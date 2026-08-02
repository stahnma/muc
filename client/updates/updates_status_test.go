package updates

import "testing"

func TestUpdateResultStatus(t *testing.T) {
	tests := []struct {
		name          string
		result        UpdateResult
		wantAvailable bool
		wantUnknown   bool
	}{
		{
			name:          "no manager detected is unknown",
			result:        UpdateResult{ManagerDetected: false},
			wantAvailable: false,
			wantUnknown:   true,
		},
		{
			name:          "manager detected with pending updates is available",
			result:        UpdateResult{ManagerDetected: true, Updates: []Update{{Name: "openssl"}}},
			wantAvailable: true,
			wantUnknown:   false,
		},
		{
			// This is the smallboi bug: dnf is present but check-update failed
			// (read-only cache/log under a hardened sandbox). An empty list here
			// must report unknown, not "up to date".
			name:          "check failed with empty list is unknown, not up to date",
			result:        UpdateResult{ManagerDetected: true, CheckFailed: true},
			wantAvailable: false,
			wantUnknown:   true,
		},
		{
			name:          "manager detected, check ok, nothing pending is up to date",
			result:        UpdateResult{ManagerDetected: true},
			wantAvailable: false,
			wantUnknown:   false,
		},
		{
			// A successful list of updates is reported even if some other
			// detected manager's check failed.
			name:          "pending updates win over a concurrent check failure",
			result:        UpdateResult{ManagerDetected: true, CheckFailed: true, Updates: []Update{{Name: "vim"}}},
			wantAvailable: true,
			wantUnknown:   false,
		},
		{
			// dnf's skip_if_unavailable turns an unreachable or unverifiable
			// repo into a warning and a clean exit, so an empty list here could
			// equally mean "nothing pending" or "the skipped repo had them".
			name:          "skipped repos with empty list is unknown, not up to date",
			result:        UpdateResult{ManagerDetected: true, SkippedRepos: []string{"grafana"}},
			wantAvailable: false,
			wantUnknown:   true,
		},
		{
			// Asymmetry on purpose: those updates really are pending regardless
			// of what the skipped repo would have added, so report them and
			// surface the skip as a warning rather than blanking the host out.
			name:          "pending updates are still reported when a repo was skipped",
			result:        UpdateResult{ManagerDetected: true, SkippedRepos: []string{"grafana"}, Updates: []Update{{Name: "openssl"}}},
			wantAvailable: true,
			wantUnknown:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAvailable, gotUnknown := tt.result.Status()
			if gotAvailable != tt.wantAvailable {
				t.Errorf("available = %v, want %v", gotAvailable, tt.wantAvailable)
			}
			if gotUnknown != tt.wantUnknown {
				t.Errorf("unknown = %v, want %v", gotUnknown, tt.wantUnknown)
			}
		})
	}
}
