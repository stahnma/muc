package updates

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestDnfCheckUpdateArgsForcesRefresh guards against the staleness regression
// where the client under-reported updates (dashboard showed 2, `dnf update`
// showed 3) because dnf answered from a stale metadata cache. The primary
// check-update invocation must shorten metadata_expire so dnf re-syncs.
//
// The "*." repoid glob is the part that actually works: a bare
// --setopt=metadata_expire= only writes dnf's [main] section, which per-repo
// settings in /etc/yum.repos.d override. RHEL/Rocky/CentOS ship
// metadata_expire=6h in their stock repo files, so the unprefixed form is a
// silent no-op on the base repos. Verified on Rocky 10 via `dnf config-manager
// --dump baseos`: unprefixed left it at 21600, the glob applied 3600.
func TestDnfCheckUpdateArgsForcesRefresh(t *testing.T) {
	args := dnfCheckUpdateArgs()

	if len(args) == 0 || args[0] != "check-update" {
		t.Fatalf("expected first arg to be check-update, got %v", args)
	}

	want := "--setopt=*.metadata_expire=" + dnfMetadataExpire
	found := false
	for _, a := range args {
		if a == want {
			found = true
		}
		if a == "--setopt=metadata_expire="+dnfMetadataExpire {
			t.Errorf("args use the unprefixed --setopt=metadata_expire=, which per-repo "+
				"settings in /etc/yum.repos.d override; use the %q repoid glob instead", want)
		}
	}
	if !found {
		t.Errorf("dnf check-update args must force a metadata refresh via %q; got %v", want, args)
	}

	if dnfMetadataExpire == "" {
		t.Error("dnfMetadataExpire must be a non-empty dnf duration")
	}
}

// TestDnfCheckUpdateArgsAcceptsRepoKeys pins the -y that lets the client adopt a
// repo's signing key unattended. Without it, a repo with repo_gpgcheck=1 whose
// key is not yet in the per-repo keyring prompts, the prompt is declined, and
// skip_if_unavailable silently drops the repo along with all of its updates.
func TestDnfCheckUpdateArgsAcceptsRepoKeys(t *testing.T) {
	args := dnfCheckUpdateArgs()

	if !slices.Contains(args, "-y") && !slices.Contains(args, "--assumeyes") {
		t.Errorf("dnf check-update args must accept repo signing keys unattended; got %v", args)
	}
	if slices.Contains(args, "--assumeno") {
		t.Errorf("--assumeno declines the key import and silently drops the repo; got %v", args)
	}
}

// TestParseDnfImportedKeys covers the audit trail for keys adopted under -y.
// Nothing else records that the host's trusted set changed.
func TestParseDnfImportedKeys(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   []string
	}{
		{name: "no imports", stderr: "Last metadata expiration check: 0:03:11 ago.", want: nil},
		{
			name: "single import",
			stderr: `Importing GPG key 0x10458545:
 Userid     : "Grafana Labs <engineering@grafana.com>"
 Fingerprint: B53A E77B ADB6 30A6 8304 6005 963F A277 1045 8545`,
			want: []string{"0x10458545"},
		},
		{
			name:   "several imports are deduplicated",
			stderr: "Importing GPG key 0x10458545:\nImporting GPG key 0xA621E701:\nImporting GPG key 0x10458545:",
			want:   []string{"0x10458545", "0xA621E701"},
		},
		{
			// dnf5's wording. Matching only dnf4's left the audit trail empty on
			// Fedora hosts, which is exactly where keys get adopted silently.
			name: "dnf5 says OpenPGP rather than GPG",
			stderr: `>>> repomd.xml GPG signature verification error: Signing key not found
Importing OpenPGP key 0x10458545:
 UserID     : "Grafana Labs <engineering@grafana.com>"
The key was successfully imported.`,
			want: []string{"0x10458545"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDnfImportedKeys(tt.stderr); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDnfImportedKeys() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDnfCheckUpdateArgsUsesPersistentCache verifies the client pins a cache
// directory when unprivileged. Without it dnf falls back to /var/tmp/dnf-$USER-*,
// which the unit's PrivateTmp=true destroys on every restart — so with
// Restart=always the client re-downloads tens of megabytes of repo metadata
// each time it comes back.
func TestDnfCheckUpdateArgsUsesPersistentCache(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a cache directory")
	}
	t.Setenv("STATE_DIRECTORY", t.TempDir())

	if dnfCacheDir() == "" {
		// Running as root the default /var/cache/dnf is correct and shared with
		// dnf-makecache.timer, so no cachedir override is expected.
		t.Skip("running as root; dnf's default cache dir is intentionally used")
	}

	found := false
	for _, a := range dnfCheckUpdateArgs() {
		if strings.HasPrefix(a, "--setopt=cachedir=") {
			found = true
		}
	}
	if !found {
		t.Errorf("unprivileged dnf check-update args must pin a persistent cachedir; got %v", dnfCheckUpdateArgs())
	}
}

// TestDnfCacheDirPrefersStateDirectory checks the systemd StateDirectory is used
// ahead of $HOME, and that root is left on dnf's default.
func TestDnfCacheDirPrefersStateDirectory(t *testing.T) {
	t.Setenv("HOME", "/home/somewhere")
	// systemd colon-separates STATE_DIRECTORY when several are configured.
	t.Setenv("STATE_DIRECTORY", "/var/lib/muc:/var/lib/other")

	got := dnfCacheDir()
	if got == "" {
		t.Skip("running as root; dnf's default cache dir is intentionally used")
	}
	if got != "/var/lib/muc/dnf" {
		t.Errorf("dnfCacheDir() = %q, want /var/lib/muc/dnf", got)
	}
}

// TestParseDnfSkippedRepos covers the stderr dnf emits when skip_if_unavailable
// swallows a repo failure. dnf still exits 0/100, so without scraping these the
// client reports a silently incomplete package list as a confident "up to date".
//
// The multi-line sample is verbatim from a Rocky 10 host where the grafana repo
// sets repo_gpgcheck=1: repomd verification uses a per-user GPG directory under
// the cache dir rather than the system rpm keyring, so an unprivileged dnf tries
// to import the key, cannot prompt, and drops the repo.
func TestParseDnfSkippedRepos(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   []string
	}{
		{
			name:   "clean run",
			stderr: "",
			want:   nil,
		},
		{
			name: "gpg key import failure",
			stderr: `Importing GPG key 0x10458545:
 Userid     : "Grafana Labs <engineering@grafana.com>"
 From       : https://rpm.grafana.com/gpg.key
Error: Failed to download metadata for repo 'grafana': repomd.xml GPG signature verification error: Signing key not found
Ignoring repositories: grafana`,
			want: []string{"grafana"},
		},
		{
			name:   "several repos on the ignore line",
			stderr: "Ignoring repositories: grafana, hashicorp epel",
			want:   []string{"epel", "grafana", "hashicorp"},
		},
		{
			name:   "download errors phrasing",
			stderr: "Errors during downloading metadata for repository 'extras':\n  - Curl error",
			want:   []string{"extras"},
		},
		{
			name: "deduplicated across patterns",
			stderr: `Failed to download metadata for repo 'grafana': timeout
Ignoring repositories: grafana`,
			want: []string{"grafana"},
		},
		{
			name:   "unrelated warnings are not repos",
			stderr: "Last metadata expiration check: 0:03:11 ago.\nwarning: /var/cache/dnf is not writable",
			want:   nil,
		},

		// dnf5 (Fedora 41+) shares none of the phrasings above: it reports
		// repository trouble as ">>> " lines and never names the repo. These
		// samples are verbatim from a Fedora 44 host running the client's own
		// argument set. Until they were handled, dnf5 hosts silently reported an
		// incomplete package list as a confident "up to date".
		{
			name: "dnf5 key declined means the repo really was skipped",
			stderr: `Updating and loading repositories:
>>> repomd.xml GPG signature verification error: Signing key not found
>>> repomd.xml GPG signature verification error: Signing key not found
Repositories loaded.`,
			want: []string{"repomd.xml GPG signature verification error: Signing key not found"},
		},
		{
			// The same diagnostic appears on a run that then imports the key and
			// loads the repo fine. Flagging it would raise a false warning on
			// every host's first contact with a new repo.
			name: "dnf5 key imported successfully is not a skipped repo",
			stderr: `Updating and loading repositories:
>>> repomd.xml GPG signature verification error: Signing key not found
Importing OpenPGP key 0x10458545:
 UserID     : "Grafana Labs <engineering@grafana.com>"
 From       : https://rpm.grafana.com/gpg.key
The key was successfully imported.
Repositories loaded.`,
			want: nil,
		},
		{
			name: "dnf5 unreachable repo is named by host",
			stderr: `Updating and loading repositories:
>>> Curl error (7): Could not connect to server for http://mirror.example.com/repodata/repomd.xml [Failed to connect] - http://mirror.example.com/repodata/repomd.xml
>>> Curl error (7): Could not connect to server for http://mirror.example.com/repodata/repomd.xml [Failed to connect] - http://mirror.example.com/repodata/repomd.xml
Repositories loaded.`,
			want: []string{"mirror.example.com (Curl error (7): Could not connect to server)"},
		},
		{
			// A bad signature is not recoverable by importing a key, so even
			// alongside a successful import elsewhere it must still be reported.
			name: "dnf5 bad signature is reported even when another key imported",
			stderr: `>>> repomd.xml GPG signature verification error: Bad PGP signature
The key was successfully imported.`,
			want: []string{"repomd.xml GPG signature verification error: Bad PGP signature"},
		},
		{
			name:   "dnf5 clean run",
			stderr: "Updating and loading repositories:\nRepositories loaded.",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDnfSkippedRepos(tt.stderr)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDnfSkippedRepos() = %v, want %v", got, tt.want)
			}
		})
	}
}
