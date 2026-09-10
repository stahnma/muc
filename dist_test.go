package main

import (
	"os"
	"strings"
	"testing"
)

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	if len(data) == 0 {
		t.Fatalf("%s is empty", path)
	}
	return string(data)
}

func assertContains(t *testing.T, content, substr, msg string) {
	t.Helper()
	if !strings.Contains(content, substr) {
		t.Errorf("%s (looking for %q)", msg, substr)
	}
}

func TestServerSystemdUnit(t *testing.T) {
	content := readFileOrFail(t, "server/dist/muc-server.service")

	assertContains(t, content, "[Unit]", "missing [Unit] section")
	assertContains(t, content, "[Service]", "missing [Service] section")
	assertContains(t, content, "[Install]", "missing [Install] section")
	assertContains(t, content, "ExecStart=", "missing ExecStart directive")
	assertContains(t, content, "Type=", "missing Type directive")
	assertContains(t, content, "User=muc", "missing User=muc")
	assertContains(t, content, "StateDirectory=muc", "missing StateDirectory=muc")
}

func TestClientSystemdUnit(t *testing.T) {
	content := readFileOrFail(t, "client/dist/muc-client.service")

	assertContains(t, content, "[Unit]", "missing [Unit] section")
	assertContains(t, content, "[Service]", "missing [Service] section")
	assertContains(t, content, "[Install]", "missing [Install] section")
	assertContains(t, content, "ExecStart=", "missing ExecStart directive")
	assertContains(t, content, "Type=", "missing Type directive")
	assertContains(t, content, "Restart=always", "missing Restart=always")
	// muc-client-recheck.service reloads the client after a package transaction;
	// without ExecReload that reload is a no-op and the dashboard only catches up
	// on the next poll.
	assertContains(t, content, "ExecReload=", "missing ExecReload; muc-client-recheck.service cannot trigger a re-check")
	assertContains(t, content, "USR1", "ExecReload must send SIGUSR1, which the client handles as 're-check now'")

	// Updates the client runs itself (rather than through a transient unit, which
	// needs systemd) are its children, in this unit's control group. Under the
	// default KillMode=control-group, a transaction that upgrades muc-client
	// restarts the unit and kills the package manager partway through — measured:
	// a forked child dies on restart even in its own session, since setsid does
	// not escape the cgroup.
	assertContains(t, content, "KillMode=process",
		"client unit must not kill its children on restart: a self-upgrading transaction would take the package manager with it")

	// The client asks the system package manager what is pending, and every
	// packaged target needs root for a trustworthy answer: apt and zypper cannot
	// refresh metadata otherwise, and dnf/yum would answer from a per-user cache
	// the administrator's `sudo dnf` never reads — fresher than the shell's, so
	// the dashboard would report real updates that `dnf update` denies exist.
	assertContains(t, content, "User=root", "client must run as root; an unprivileged dnf reads a different metadata cache than the administrator's shell")

	// No StateDirectory. The client writes nothing, and systemd re-chowns a
	// StateDirectory to the unit's user on every start, recursively — so a root
	// client sharing /var/lib/muc with muc-server (which runs as muc and keeps
	// systems.db there) takes the directory away from it. The server does not
	// fail immediately: it holds a writable fd opened before the chown, and
	// permission is checked at open(), not per write. It fails on its next
	// restart, looking unrelated to whatever touched the client.
	if strings.Contains(content, "StateDirectory=") {
		t.Error("client unit must not set StateDirectory: it writes nothing, and systemd would chown /var/lib/muc away from muc-server")
	}
}

// TestClientPostinstallRetiresDropInAndRepairsState covers the upgrade path off
// the old "run as muc, override to root per package manager" arrangement.
//
// Two things must be undone on hosts that ran that version, and neither is
// self-healing:
//
//   - the 10-root.conf drop-in. The unit sets User=root itself now, so a stale
//     override would keep applying settings this package no longer manages —
//     including the StateDirectory=muc that causes the second problem.
//   - ownership of /var/lib/muc. systemd re-chowns a StateDirectory to the
//     unit's user on every start, recursively, so every host that ran this
//     client as root has a root-owned /var/lib/muc. Where muc-server is
//     installed alongside, that is its database directory and it runs as muc.
func TestClientPostinstallRetiresDropInAndRepairsState(t *testing.T) {
	content := readFileOrFail(t, "client/dist/postinstall.sh")

	assertContains(t, content, "rm -f \"$DROPIN_DIR/10-root.conf\"",
		"must remove the drop-in the shipped unit has superseded")
	// Older packages shipped it under a zypper-specific name.
	assertContains(t, content, "10-zypper-root.conf",
		"must clean up the legacy drop-in name from older packages")
	assertContains(t, content, "chown -R muc:muc /var/lib/muc",
		"must return /var/lib/muc to muc-server, which a previous root client chowned away")
	assertContains(t, content, "rm -rf /var/lib/muc/dnf",
		"must reclaim the metadata cache the unprivileged client left behind")

	// Retiring the leftover directory on client-only hosts must never be able to
	// take the server's database with it. rmdir refuses a non-empty directory;
	// rm -r would not, and systems.db lives there on a combined host.
	assertContains(t, content, "rmdir /var/lib/muc",
		"must retire the empty leftover directory on client-only hosts")
	if strings.Contains(content, "rm -rf /var/lib/muc\n") || strings.Contains(content, "rm -rf /var/lib/muc ") {
		t.Error("must not rm -r /var/lib/muc: on a combined host that is muc-server's database directory")
	}
	assertContains(t, content, "/usr/lib/systemd/system/muc-server.service",
		"removal must be guarded on muc-server not being installed")
	// System users are not this package's to remove: muc-server creates the same
	// user, and UID reuse silently reassigns any file still owned by it.
	if strings.Contains(content, "userdel") || strings.Contains(content, "groupdel") {
		t.Error("client postinstall must not remove the muc user: muc-server may need it, and UID reuse reassigns orphaned files")
	}

	// The client must not write a drop-in any more: doing so would reintroduce
	// the StateDirectory that takes /var/lib/muc from muc-server. Match the
	// literal unit-section header a written drop-in must contain, at the start of
	// a line — the prose above explains the old User=root arrangement and should
	// not trip this.
	if strings.Contains(content, "\n[Service]") {
		t.Error("postinstall must not write a systemd drop-in; the shipped unit sets User=root directly")
	}
}

// TestClientPreinstallCreatesNoUser records that the client package owns no
// system user and no state directory.
//
// The client runs as root and its only filesystem write was the unprivileged dnf
// metadata cache, which root does not use — so the muc user and /var/lib/muc
// belong solely to muc-server. Creating them here would also mean chowning a
// directory the server owns, on a host running both.
func TestClientPreinstallCreatesNoUser(t *testing.T) {
	content := readFileOrFail(t, "client/dist/preinstall.sh")

	for _, cmd := range []string{"useradd", "groupadd", "chown"} {
		if strings.Contains(content, cmd) {
			t.Errorf("client preinstall must not run %s: the muc user and /var/lib/muc belong to muc-server", cmd)
		}
	}
}

// TestClientRecheckUnits covers the path/service pair that makes the dashboard
// reflect a package transaction in seconds instead of at the next poll.
func TestClientRecheckUnits(t *testing.T) {
	pathUnit := readFileOrFail(t, "client/dist/muc-client-recheck.path")

	assertContains(t, pathUnit, "[Path]", "missing [Path] section")
	assertContains(t, pathUnit, "[Install]", "missing [Install] section")
	assertContains(t, pathUnit, "WantedBy=paths.target", "path units must be wanted by paths.target")
	assertContains(t, pathUnit, "Unit=muc-client-recheck.service", "path unit must trigger the recheck service")
	// Watch both package-database families: one package builds for rpm and deb.
	assertContains(t, pathUnit, "/var/lib/rpm", "missing rpm database watch")
	assertContains(t, pathUnit, "/var/lib/dpkg", "missing dpkg database watch")

	svcUnit := readFileOrFail(t, "client/dist/muc-client-recheck.service")

	assertContains(t, svcUnit, "Type=oneshot", "recheck service must be a oneshot, not a daemon")
	assertContains(t, svcUnit, "reload muc-client", "recheck service must reload muc-client")
	// Without Requisite= the path unit starts a failing job on every package
	// transaction when the client is not running.
	assertContains(t, svcUnit, "Requisite=muc-client.service", "missing Requisite guard on muc-client.service")

	// Without this, systemd's default start limit (5 starts in 10s) is hit
	// during any sizeable transaction — the path unit fires per write to the
	// package database — and both units latch into a failed state that survives
	// until someone resets them. The re-check feature then stays dead.
	assertContains(t, svcUnit, "StartLimitIntervalSec=0",
		"recheck service must disable the start limit; a package transaction triggers it faster than the default allows")
}

func TestShellScripts(t *testing.T) {
	scripts := []struct {
		path        string
		mustContain []string
	}{
		{"server/dist/preinstall.sh", []string{"groupadd", "useradd"}},
		{"server/dist/postinstall.sh", []string{"daemon-reload"}},
		{"server/dist/preremove.sh", []string{"systemctl stop"}},
		// The client package intentionally creates no user; see
		// TestClientPreinstallCreatesNoUser.
		{"client/dist/preinstall.sh", nil},
		{"client/dist/postinstall.sh", []string{"daemon-reload", "muc-client-recheck.path", "reset-failed"}},
		{"client/dist/preremove.sh", []string{"systemctl stop"}},
		{"client/dist/postremove.sh", []string{"muc-client-recheck.path"}},
	}
	for _, s := range scripts {
		t.Run(s.path, func(t *testing.T) {
			content := readFileOrFail(t, s.path)

			if !strings.HasPrefix(content, "#!/bin/sh") {
				t.Error("missing #!/bin/sh shebang")
			}

			info, err := os.Stat(s.path)
			if err != nil {
				t.Fatalf("cannot stat %s: %v", s.path, err)
			}
			if info.Mode()&0111 == 0 {
				t.Errorf("script %s is not executable", s.path)
			}

			for _, must := range s.mustContain {
				assertContains(t, content, must, "missing: "+must)
			}
		})
	}
}

func TestConfigFiles(t *testing.T) {
	configs := []string{
		"server/dist/config.yml",
		"client/dist/client.yml",
	}
	for _, path := range configs {
		t.Run(path, func(t *testing.T) {
			content := readFileOrFail(t, path)

			lines := strings.Split(content, "\n")
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				// skip empty lines and comments
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				// non-comment, non-empty lines should either:
				// - start with whitespace (continuation/indented value)
				// - contain a colon (key: value or key:)
				if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.Contains(line, ":") {
					t.Errorf("line %d does not look like valid YAML: %q", i+1, line)
				}
			}
		})
	}
}

// TestUpdateScriptIsShipped covers the script the remote-update feature runs.
//
// The client resolves /usr/libexec/muc/upd first, so a host that opted into
// remote updates but never had upd installed by hand still has something to
// run. The mode matters as much as the path: the client refuses a command that
// is not executable rather than handing it to a shell.
func TestUpdateScriptIsShipped(t *testing.T) {
	content := readFileOrFail(t, "client/scripts/upd")
	if !strings.HasPrefix(content, "#!") {
		t.Error("client/scripts/upd has no shebang; the client execs it directly")
	}

	info, err := os.Stat("client/scripts/upd")
	if err != nil {
		t.Fatalf("cannot stat client/scripts/upd: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("client/scripts/upd is not executable")
	}

	release := readFileOrFail(t, ".goreleaser.yml")
	assertContains(t, release, "src: client/scripts/upd",
		"the client package must ship the update script")
	assertContains(t, release, "dst: /usr/libexec/muc/upd",
		"the update script must land where the client looks for it first")
	assertContains(t, release, "mode: 0755",
		"the update script must be installed executable")
}

// TestClientPackageRecommendsNeedsRestarting pins the reboot-detection
// dependency, and pins it as a weak one.
//
// hostinfo.RebootRequired asks needs-restarting on rpm systems; without it the
// answer falls back to comparing the running kernel against the newest
// installed one, which cannot see a glibc or systemd update at all. Hosts then
// under-report reboots silently, which is the failure mode nobody notices.
//
// It must stay a recommendation. One rpm is built for every rpm distro, and
// SUSE packages no needs-restarting at all — the client uses `zypper
// needs-rebooting` there — so a hard requirement would make the package
// uninstallable on a platform it otherwise supports. dnf installs weak
// dependencies by default, so EL and Fedora get it anyway.
//
// The dependency is on the file, not on dnf-utils, because the package owning
// it moves: EL8/EL9 have a real dnf-utils, EL10 ships the command in yum-utils
// (which only Provides dnf-utils), and Fedora's dnf5 has moved it again.
func TestClientPackageRecommendsNeedsRestarting(t *testing.T) {
	release := readFileOrFail(t, ".goreleaser.yml")

	const needsRestarting = "/usr/bin/needs-restarting"
	assertContains(t, release, needsRestarting,
		"the client rpm must pull in needs-restarting; without it reboot detection can only compare kernel versions")

	// The entry has to sit directly under a recommends: key. Listed as a
	// dependency instead, it would be a hard Requires.
	lines := strings.Split(release, "\n")
	weak := false
	for i, line := range lines {
		if strings.TrimSpace(line) != "recommends:" || i+1 >= len(lines) {
			continue
		}
		if strings.Contains(lines[i+1], needsRestarting) {
			weak = true
		}
	}
	if !weak {
		t.Errorf("%s must be listed under recommends:, not as a hard dependency: the same rpm installs on SUSE, which packages no such command", needsRestarting)
	}
}

// TestClientUnitStaysSandboxed records why remote updates run in a transient
// systemd unit rather than as a child of the client.
//
// ProtectSystem=full makes /usr read-only, which a package manager cannot work
// under, and restarting muc-client.service kills everything in its cgroup —
// including a dnf transaction that is halfway through upgrading muc-client
// itself. Relaxing the unit to fix the first problem would leave the second,
// so the sandbox stays and updateArgv wraps the run in systemd-run.
func TestClientUnitStaysSandboxed(t *testing.T) {
	content := readFileOrFail(t, "client/dist/muc-client.service")

	assertContains(t, content, "ProtectSystem=full",
		"client sandbox must stay: remote updates escape it through systemd-run, not by loosening it")
}
