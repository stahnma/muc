package updates

import (
	"reflect"
	"testing"
)

// TestParseDnfUpdates covers the line-by-line parsing of `dnf check-update`
// stdout. dnf interleaves progress, mirror status and metadata notices with the
// package table and offers no machine-readable mode we can rely on across dnf4
// and dnf5, so this heuristic decides what lands on the dashboard.
//
// The bias is deliberate and asserted here: a missed package under-reports, but
// a false positive puts a package that does not exist on someone's dashboard.
func TestParseDnfUpdates(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []Update
	}{
		{
			name: "empty output means nothing pending",
			out:  "",
			want: nil,
		},
		{
			name: "typical check-update output",
			out: `Last metadata expiration check: 0:03:11 ago on Fri 31 Jul 2026 07:09:24 PM EDT.

bash.x86_64                     5.2.26-4.el10                      baseos
kernel-core.x86_64              6.12.0-211.40.1.el10_2             baseos
openssl-libs.x86_64             1:3.2.2-6.el10_2                   appstream
`,
			want: []Update{
				{Name: "bash.x86_64", Version: "5.2.26-4.el10", Source: "baseos"},
				{Name: "kernel-core.x86_64", Version: "6.12.0-211.40.1.el10_2", Source: "baseos"},
				{Name: "openssl-libs.x86_64", Version: "1:3.2.2-6.el10_2", Source: "appstream"},
			},
		},
		{
			name: "obsoleting section header and its indented body are not packages",
			out: `Obsoleting Packages
python3-foo.noarch              2.0-1.el10                         appstream
`,
			// The header has 2 fields so it is dropped; the package under it is
			// still a real pending change and is kept.
			want: []Update{
				{Name: "python3-foo.noarch", Version: "2.0-1.el10", Source: "appstream"},
			},
		},
		{
			name: "download progress lines are rejected",
			out: `Rocky Linux 10 - BaseOS                         3.2 MB/s | 2.1 MB     00:00
Rocky Linux 10 - AppStream                      1.1 MB/s | 8.4 MB     00:07
bash.x86_64                     5.2.26-4.el10                      baseos
`,
			want: []Update{
				{Name: "bash.x86_64", Version: "5.2.26-4.el10", Source: "baseos"},
			},
		},
		{
			name: "metadata notice is skipped",
			out:  "Last metadata expiration check: 0:03:11 ago on Fri 31 Jul 2026 07:09:24 PM EDT.\n",
			want: nil,
		},
		{
			name: "lines with fewer than three fields are skipped",
			out:  "bash.x86_64    5.2.26-4.el10\nSecurity: kernel-core updated\n",
			want: nil,
		},
		{
			name: "a version without a dot or dash is rejected",
			out:  "somepkg.noarch    12345    baseos\n",
			want: nil,
		},
		{
			name: "leading and trailing whitespace is tolerated",
			out:  "   bash.x86_64      5.2.26-4.el10      baseos   \n",
			want: []Update{
				{Name: "bash.x86_64", Version: "5.2.26-4.el10", Source: "baseos"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDnfUpdates([]byte(tt.out))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDnfUpdates() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestIsRepoStatusLine(t *testing.T) {
	statusLines := []string{
		"Rocky Linux 10 - BaseOS                  3.2 MB/s | 2.1 MB     00:00",
		"Extra Packages for Enterprise Linux      890 kB/s |  20 MB     00:23",
		"Last metadata expiration check: 0:03:11 ago.",
		"Downloading Packages:",
	}
	for _, line := range statusLines {
		if !isRepoStatusLine(line) {
			t.Errorf("isRepoStatusLine(%q) = false, want true", line)
		}
	}

	packageLines := []string{
		"bash.x86_64                     5.2.26-4.el10          baseos",
		"openssl-libs.x86_64             1:3.2.2-6.el10_2       appstream",
	}
	for _, line := range packageLines {
		if isRepoStatusLine(line) {
			t.Errorf("isRepoStatusLine(%q) = true, want false; a real package line was discarded", line)
		}
	}
}

func TestIsValidPackageName(t *testing.T) {
	valid := []string{"bash", "bash.x86_64", "kernel-core.x86_64", "python3-dnf", "lib_foo", "389-ds-base"}
	for _, name := range valid {
		if !isValidPackageName(name) {
			t.Errorf("isValidPackageName(%q) = false, want true", name)
		}
	}

	invalid := []string{
		"",            // empty
		"BASEOS",      // all uppercase, not a package name
		"3.2",         // no lowercase letter
		".leadingdot", // must start alphanumeric
		"has space",   // never reached as one field, but the rule holds
		"pkg/with",    // slash is not a legal character
		"pkg:with",    // colon is not a legal character
	}
	for _, name := range invalid {
		if isValidPackageName(name) {
			t.Errorf("isValidPackageName(%q) = true, want false", name)
		}
	}
}

func TestIsValidVersion(t *testing.T) {
	valid := []string{"5.2.26-4.el10", "1:3.2.2-6.el10_2", "6.12.0-211.40.1.el10_2", "2.0-1"}
	for _, v := range valid {
		if !isValidVersion(v) {
			t.Errorf("isValidVersion(%q) = false, want true", v)
		}
	}

	invalid := []string{
		"12345",    // no dot or dash
		"MB",       // no dot or dash
		"1.2-rpm",  // reserved keyword
		"metadata", // reserved keyword
	}
	for _, v := range invalid {
		if isValidVersion(v) {
			t.Errorf("isValidVersion(%q) = true, want false", v)
		}
	}
}

func TestIsValidRepository(t *testing.T) {
	valid := []string{"baseos", "appstream", "epel", "rancher-k3s-common-stable", "grafana"}
	for _, r := range valid {
		if !isValidRepository(r) {
			t.Errorf("isValidRepository(%q) = false, want true", r)
		}
	}

	invalid := []string{
		"2.1",      // a pure number is a size column, not a repo
		"rpms",     // reserved keyword
		"METADATA", // all uppercase
	}
	for _, r := range invalid {
		if isValidRepository(r) {
			t.Errorf("isValidRepository(%q) = true, want false", r)
		}
	}

	// Documenting a real gap rather than asserting the behaviour we wish it had:
	// this validator accepts a bare timestamp, because it is not parseable as a
	// float and contains no letters to trip the uppercase or keyword rules. Such
	// a line is caught upstream by isRepoStatusLine instead, which is the only
	// reason a download-progress line never reaches here. If that guard is ever
	// loosened, this is where a bogus "update" gets in.
	if !isValidRepository("00:00") {
		t.Log("isValidRepository now rejects bare timestamps; the isRepoStatusLine guard is no longer load-bearing here")
	}
}
