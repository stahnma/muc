package updates

import "testing"

func TestParseZypperUpdates(t *testing.T) {
	// A representative --xmlout list-updates document: info messages, package
	// updates, and a non-package kind (patch) that must be dropped.
	const xmlOut = `<?xml version='1.0'?>
<stream>
<message type="info">Loading repository data...</message>
<message type="info">Reading installed packages...</message>
<update-status version="0.6">
<update-list>
<update kind="package" name="bash" edition="5.3.15-8.1" arch="x86_64" edition-old="5.3.15-7.1"><source url="http://download.opensuse.org/tumbleweed/repo/oss/" alias="repo-oss"/></update>
<update kind="package" name="curl" edition="8.21.0-1.1" arch="x86_64" edition-old="8.20.0-1.1"><source url="http://download.opensuse.org/tumbleweed/repo/oss/" alias="repo-oss"/></update>
<update kind="patch" name="openSUSE-2026-1" edition="1" arch="noarch"><source url="http://.../update" alias="repo-update"/></update>
</update-list>
</update-status>
</stream>`

	updates, err := parseZypperUpdates([]byte(xmlOut))
	if err != nil {
		t.Fatalf("parseZypperUpdates returned error: %v", err)
	}

	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2 (patch kind should be dropped): %+v", len(updates), updates)
	}

	want := []Update{
		{Name: "bash", Version: "5.3.15-8.1", Source: "repo-oss"},
		{Name: "curl", Version: "8.21.0-1.1", Source: "repo-oss"},
	}
	for i, w := range want {
		if updates[i] != w {
			t.Errorf("update[%d] = %+v, want %+v", i, updates[i], w)
		}
	}
}

func TestParseZypperUpdatesEmpty(t *testing.T) {
	const xmlOut = `<?xml version='1.0'?>
<stream>
<update-status version="0.6">
<update-list>
</update-list>
</update-status>
</stream>`

	updates, err := parseZypperUpdates([]byte(xmlOut))
	if err != nil {
		t.Fatalf("parseZypperUpdates returned error: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("got %d updates, want 0: %+v", len(updates), updates)
	}
}

func TestParseZypperUpdatesMalformed(t *testing.T) {
	if _, err := parseZypperUpdates([]byte("not xml at all <")); err == nil {
		t.Fatal("expected an error for malformed XML, got nil")
	}
}
