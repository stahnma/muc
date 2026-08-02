package updates

import "runtime"

type Update struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
}

// UpdateResult contains the list of updates and whether a package manager was detected
type UpdateResult struct {
	Updates         []Update
	ManagerDetected bool
	// CheckFailed is set when a package manager was detected but the command to
	// query updates failed to run (e.g. exited non-zero for a reason other than
	// "updates available"). When true, an empty Updates list means "we don't
	// know", NOT "up to date".
	CheckFailed bool
	// SkippedRepos names repositories the package manager could not read and
	// silently ignored — dnf's skip_if_unavailable is the usual source, and it
	// still exits cleanly. The Updates list is then incomplete: trustworthy as
	// a lower bound, but not as a "nothing pending" answer.
	SkippedRepos []string
}

// Status derives the reportable update status from a result.
//
// available is true only when a package manager was detected, its check ran
// successfully, and at least one update is pending. unknown is true when no
// manager was detected, a detected manager's check failed to run, or the check
// succeeded but silently skipped a repository and found nothing — in all of
// those cases an empty update list means "we don't know", NOT "up to date".
// Only the case "manager detected, check succeeded, nothing skipped, nothing
// pending" reports a confident, current system (available=false, unknown=false).
//
// Note the asymmetry for skipped repos: a non-empty list is still reported as
// available, because those updates really are pending regardless of what the
// skipped repo would have added. One broken third-party repo should surface as
// a warning next to an accurate count, not blank the host out to "unknown".
func (r UpdateResult) Status() (available bool, unknown bool) {
	switch {
	case !r.ManagerDetected:
		return false, true
	case len(r.Updates) > 0:
		return true, false
	case r.CheckFailed:
		return false, true
	case len(r.SkippedRepos) > 0:
		return false, true
	default:
		return false, false
	}
}

// GetPendingUpdates determines the OS and delegates to the appropriate function
func GetPendingUpdates() UpdateResult {
	switch runtime.GOOS {
	case "linux":
		return getLinuxUpdates()
	case "darwin":
		return getBrewUpdates()
	default:
		return UpdateResult{
			Updates:         nil,
			ManagerDetected: false,
		}
	}
}

// Helper for Linux systems to gather updates from various package managers
func getLinuxUpdates() UpdateResult {
	// Every manager is probed; each returns ManagerDetected=false when it is not
	// installed on this host. Hosts with two managers really do exist, so the
	// results are merged rather than short-circuited on the first hit.
	return mergeUpdateResults(
		getAptUpdates(),
		getDnfUpdates(),
		getYumUpdates(),
		getZypperUpdates(),
		getNixosUpdates(),
	)
}

// mergeUpdateResults combines the per-manager results into the single answer
// reported for the host.
//
// Results from undetected managers are discarded entirely — including any
// Updates they may have populated — so an absent manager cannot contribute.
// CheckFailed and SkippedRepos accumulate across managers: if any detected
// manager could not give a complete answer, the host's answer is incomplete too.
func mergeUpdateResults(results ...UpdateResult) UpdateResult {
	var merged UpdateResult

	for _, result := range results {
		if !result.ManagerDetected {
			continue
		}
		merged.ManagerDetected = true
		merged.CheckFailed = merged.CheckFailed || result.CheckFailed
		merged.Updates = append(merged.Updates, result.Updates...)
		merged.SkippedRepos = append(merged.SkippedRepos, result.SkippedRepos...)
	}

	return merged
}
