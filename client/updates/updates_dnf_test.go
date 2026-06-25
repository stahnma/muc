package updates

import "testing"

// TestDnfCheckUpdateArgsForcesRefresh guards against the staleness regression
// where the client under-reported updates (dashboard showed 2, `dnf update`
// showed 3) because dnf answered from a stale metadata cache. The primary
// check-update invocation must shorten metadata_expire so dnf re-syncs.
func TestDnfCheckUpdateArgsForcesRefresh(t *testing.T) {
	args := dnfCheckUpdateArgs()

	if len(args) == 0 || args[0] != "check-update" {
		t.Fatalf("expected first arg to be check-update, got %v", args)
	}

	want := "--setopt=metadata_expire=" + dnfMetadataExpire
	found := false
	for _, a := range args {
		if a == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("dnf check-update args must force a metadata refresh via %q; got %v", want, args)
	}

	if dnfMetadataExpire == "" {
		t.Error("dnfMetadataExpire must be a non-empty dnf duration")
	}
}
