package updates

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fakeDnf records the argument sets it was called with and replays a scripted
// sequence of responses, one per call.
type fakeDnf struct {
	calls   [][]string
	replies []dnfReply
}

type dnfReply struct {
	stdout string
	stderr string
	err    error
}

func (f *fakeDnf) run(args ...string) ([]byte, string, error) {
	f.calls = append(f.calls, args)
	if len(f.calls) > len(f.replies) {
		return nil, "", errors.New("unexpected extra dnf invocation")
	}
	r := f.replies[len(f.calls)-1]
	return []byte(r.stdout), r.stderr, r.err
}

const sampleDnfOutput = "bash.x86_64    5.2.26-4.el10    baseos\n"

var sampleDnfUpdate = Update{Name: "bash.x86_64", Version: "5.2.26-4.el10", Source: "baseos"}

// TestCollectDnfUpdatesRetryLadder covers the fallback chain. Each rung must
// keep the freshness flags for as long as it can: silently dropping them is the
// staleness bug this path exists to prevent, and it is invisible at runtime
// because dnf still exits cleanly.
func TestCollectDnfUpdatesRetryLadder(t *testing.T) {
	boom := errors.New("dnf failed")

	t.Run("first attempt succeeds", func(t *testing.T) {
		f := &fakeDnf{replies: []dnfReply{{stdout: sampleDnfOutput}}}

		got := collectDnfUpdates(f.run)

		if len(f.calls) != 1 {
			t.Fatalf("expected exactly 1 dnf invocation, got %d: %v", len(f.calls), f.calls)
		}
		if !slices.Contains(f.calls[0], "--setopt=skip_if_unavailable=True") {
			t.Errorf("first attempt must pass skip_if_unavailable; got %v", f.calls[0])
		}
		if !reflect.DeepEqual(got.Updates, []Update{sampleDnfUpdate}) {
			t.Errorf("Updates = %#v", got.Updates)
		}
		if got.CheckFailed || len(got.SkippedRepos) != 0 {
			t.Errorf("clean run must not be flagged: CheckFailed=%v SkippedRepos=%v", got.CheckFailed, got.SkippedRepos)
		}
	})

	t.Run("second attempt keeps the freshness flags", func(t *testing.T) {
		f := &fakeDnf{replies: []dnfReply{
			{err: boom},
			{stdout: sampleDnfOutput},
		}}

		got := collectDnfUpdates(f.run)

		if len(f.calls) != 2 {
			t.Fatalf("expected 2 dnf invocations, got %d: %v", len(f.calls), f.calls)
		}
		retry := f.calls[1]
		if !slices.Contains(retry, dnfMetadataExpireSetopt) {
			t.Errorf("retry dropped the metadata_expire pin, silently reverting to each repo's 6h+ default; got %v", retry)
		}
		if slices.Contains(retry, "--setopt=skip_if_unavailable=True") {
			t.Errorf("retry should drop skip_if_unavailable, the option most likely to have caused the failure; got %v", retry)
		}
		// A successful retry is still a fully trustworthy answer.
		if len(got.SkippedRepos) != 0 {
			t.Errorf("retry that kept its freshness flags must not be flagged degraded; got %v", got.SkippedRepos)
		}
		if !reflect.DeepEqual(got.Updates, []Update{sampleDnfUpdate}) {
			t.Errorf("Updates = %#v", got.Updates)
		}
	})

	t.Run("bare fallback is recorded as degraded", func(t *testing.T) {
		f := &fakeDnf{replies: []dnfReply{
			{err: boom},
			{err: boom},
			{stdout: ""},
		}}

		got := collectDnfUpdates(f.run)

		if len(f.calls) != 3 {
			t.Fatalf("expected 3 dnf invocations, got %d: %v", len(f.calls), f.calls)
		}
		if !reflect.DeepEqual(f.calls[2], []string{"check-update"}) {
			t.Errorf("last resort must be a bare check-update; got %v", f.calls[2])
		}
		if !slices.Contains(got.SkippedRepos, dnfFreshnessNotEnforced) {
			t.Errorf("a bare fallback answered from unpinned cache and must be flagged; got %v", got.SkippedRepos)
		}
		// This is the point of the flag: an empty list from an unpinned cache
		// must not read as a confident "up to date".
		available, unknown := got.Status()
		if available || !unknown {
			t.Errorf("degraded empty result: available=%v unknown=%v, want false/true", available, unknown)
		}
	})

	t.Run("all attempts fail is unknown, not up to date", func(t *testing.T) {
		f := &fakeDnf{replies: []dnfReply{{err: boom}, {err: boom}, {err: boom}}}

		got := collectDnfUpdates(f.run)

		if !got.ManagerDetected || !got.CheckFailed {
			t.Errorf("ManagerDetected=%v CheckFailed=%v, want true/true", got.ManagerDetected, got.CheckFailed)
		}
		available, unknown := got.Status()
		if available || !unknown {
			t.Errorf("available=%v unknown=%v, want false/true", available, unknown)
		}
	})
}

// TestCollectDnfUpdatesSkippedRepos is the regression guard for the bug that
// motivated all of this: skip_if_unavailable turns an unreadable repo into a
// stderr warning and a clean exit, so an incomplete package list was being
// reported as an authoritative "up to date".
func TestCollectDnfUpdatesSkippedRepos(t *testing.T) {
	// Verbatim from a Rocky 10 host where grafana sets repo_gpgcheck=1.
	const grafanaStderr = `Error: Failed to download metadata for repo 'grafana': repomd.xml GPG signature verification error: Signing key not found
Ignoring repositories: grafana`

	t.Run("empty list plus a skipped repo is unknown", func(t *testing.T) {
		f := &fakeDnf{replies: []dnfReply{{stdout: "", stderr: grafanaStderr}}}

		got := collectDnfUpdates(f.run)

		if !reflect.DeepEqual(got.SkippedRepos, []string{"grafana"}) {
			t.Errorf("SkippedRepos = %v, want [grafana]", got.SkippedRepos)
		}
		available, unknown := got.Status()
		if available || !unknown {
			t.Errorf("available=%v unknown=%v, want false/true; this is the silent 'up to date' bug", available, unknown)
		}
	})

	t.Run("a real update list survives a skipped repo", func(t *testing.T) {
		f := &fakeDnf{replies: []dnfReply{{stdout: sampleDnfOutput, stderr: grafanaStderr}}}

		got := collectDnfUpdates(f.run)

		// Asymmetry on purpose: those updates really are pending regardless of
		// what grafana would have added, so report them and surface the skip as
		// a warning rather than blanking the host out to "unknown".
		available, unknown := got.Status()
		if !available || unknown {
			t.Errorf("available=%v unknown=%v, want true/false", available, unknown)
		}
		if !reflect.DeepEqual(got.SkippedRepos, []string{"grafana"}) {
			t.Errorf("SkippedRepos = %v, want [grafana]", got.SkippedRepos)
		}
	})
}

// TestMergeUpdateResults covers the cross-manager accumulation that
// getLinuxUpdates delegates to. A dropped SkippedRepos here would re-hide
// exactly what the rest of this work exists to surface.
func TestMergeUpdateResults(t *testing.T) {
	t.Run("no managers detected is unknown", func(t *testing.T) {
		got := mergeUpdateResults(UpdateResult{}, UpdateResult{})

		if got.ManagerDetected {
			t.Error("ManagerDetected = true, want false")
		}
		available, unknown := got.Status()
		if available || !unknown {
			t.Errorf("available=%v unknown=%v, want false/true", available, unknown)
		}
	})

	t.Run("undetected managers contribute nothing", func(t *testing.T) {
		got := mergeUpdateResults(
			UpdateResult{ManagerDetected: true, Updates: []Update{{Name: "bash"}}},
			// A manager that is not installed must not leak state, even if it
			// populated fields before deciding it was absent.
			UpdateResult{ManagerDetected: false, Updates: []Update{{Name: "ghost"}},
				CheckFailed: true, SkippedRepos: []string{"ghost-repo"}},
		)

		if !reflect.DeepEqual(got.Updates, []Update{{Name: "bash"}}) {
			t.Errorf("Updates = %#v, want just bash", got.Updates)
		}
		if got.CheckFailed {
			t.Error("an undetected manager must not set CheckFailed")
		}
		if len(got.SkippedRepos) != 0 {
			t.Errorf("SkippedRepos = %v, want empty", got.SkippedRepos)
		}
	})

	t.Run("failures and skipped repos accumulate across managers", func(t *testing.T) {
		got := mergeUpdateResults(
			UpdateResult{ManagerDetected: true, Updates: []Update{{Name: "bash"}}, SkippedRepos: []string{"grafana"}},
			UpdateResult{ManagerDetected: true, CheckFailed: true, SkippedRepos: []string{"hashicorp"}},
		)

		if !got.CheckFailed {
			t.Error("CheckFailed must survive from any detected manager")
		}
		if !reflect.DeepEqual(got.SkippedRepos, []string{"grafana", "hashicorp"}) {
			t.Errorf("SkippedRepos = %v, want [grafana hashicorp]", got.SkippedRepos)
		}
		// Pending updates from a healthy manager are still reported even though
		// another manager on the same host could not answer.
		available, unknown := got.Status()
		if !available || unknown {
			t.Errorf("available=%v unknown=%v, want true/false", available, unknown)
		}
	})
}

// TestDnfRunnerDefaultsToRealCommand guards the seam: leaving a fake installed
// would silently disable update checking in production.
func TestDnfRunnerDefaultsToRealCommand(t *testing.T) {
	if dnfRunner == nil {
		t.Fatal("dnfRunner must default to the real runner")
	}
	if !strings.HasSuffix(dnfPath, "/dnf") {
		t.Errorf("dnfPath = %q, want a path ending in /dnf", dnfPath)
	}
}
