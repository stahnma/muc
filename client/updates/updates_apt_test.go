package updates

import "testing"

func TestParseAptUpdates(t *testing.T) {
	// Representative `apt list --upgradable` output: the "Listing..." header
	// (which must be skipped) followed by two upgradable packages.
	const aptOut = `Listing...
bash/stable 5.2.15-2+deb12u1 amd64 [upgradable from: 5.2.15-2]
curl/stable-security 7.88.1-10+deb12u5 amd64 [upgradable from: 7.88.1-10+deb12u4]
`

	updates := parseAptUpdates([]byte(aptOut))

	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2: %+v", len(updates), updates)
	}

	want := []Update{
		{Name: "bash", Version: "5.2.15-2+deb12u1", Source: "stable"},
		{Name: "curl", Version: "7.88.1-10+deb12u5", Source: "stable-security"},
	}
	for i, w := range want {
		if updates[i] != w {
			t.Errorf("update[%d] = %+v, want %+v", i, updates[i], w)
		}
	}
}

func TestParseAptUpdatesEmpty(t *testing.T) {
	// A machine that is up to date emits only the header.
	updates := parseAptUpdates([]byte("Listing...\n"))
	if len(updates) != 0 {
		t.Fatalf("got %d updates, want 0: %+v", len(updates), updates)
	}
}

func TestParseAptUpdatesDeduplicatesSource(t *testing.T) {
	// apt can emit duplicate repository names after the "/"; they collapse.
	const aptOut = `Listing...
libc6/oldoldstable-security,oldoldstable-security 2.28-10+deb10u2 amd64 [upgradable from: 2.28-10]
`
	updates := parseAptUpdates([]byte(aptOut))
	if len(updates) != 1 {
		t.Fatalf("got %d updates, want 1: %+v", len(updates), updates)
	}
	if updates[0].Source != "oldoldstable-security" {
		t.Errorf("source = %q, want %q", updates[0].Source, "oldoldstable-security")
	}
}
