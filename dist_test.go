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
	assertContains(t, content, "User=muc", "missing User=muc")
	assertContains(t, content, "Restart=always", "missing Restart=always")
	// muc-client-recheck.service reloads the client after a package transaction;
	// without ExecReload that reload is a no-op and the dashboard only catches up
	// on the next poll.
	assertContains(t, content, "ExecReload=", "missing ExecReload; muc-client-recheck.service cannot trigger a re-check")
	assertContains(t, content, "USR1", "ExecReload must send SIGUSR1, which the client handles as 're-check now'")
	// The client pins dnf's cache to $STATE_DIRECTORY. Without StateDirectory an
	// unprivileged dnf falls back to a /var/tmp dir that PrivateTmp destroys on
	// every restart, forcing a full metadata re-download each time.
	assertContains(t, content, "StateDirectory=muc", "missing StateDirectory=muc")
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
}

func TestShellScripts(t *testing.T) {
	scripts := []struct {
		path        string
		mustContain []string
	}{
		{"server/dist/preinstall.sh", []string{"groupadd", "useradd"}},
		{"server/dist/postinstall.sh", []string{"daemon-reload"}},
		{"server/dist/preremove.sh", []string{"systemctl stop"}},
		{"client/dist/preinstall.sh", []string{"groupadd", "useradd"}},
		{"client/dist/postinstall.sh", []string{"daemon-reload", "muc-client-recheck.path"}},
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
