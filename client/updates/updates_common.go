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
}

// Status derives the reportable update status from a result.
//
// available is true only when a package manager was detected, its check ran
// successfully, and at least one update is pending. unknown is true when no
// manager was detected, or a detected manager's check failed to run — in both
// of those cases an empty update list means "we don't know", NOT "up to date".
// Only the case "manager detected, check succeeded, nothing pending" reports a
// confident, current system (available=false, unknown=false).
func (r UpdateResult) Status() (available bool, unknown bool) {
	switch {
	case !r.ManagerDetected:
		return false, true
	case len(r.Updates) > 0:
		return true, false
	case r.CheckFailed:
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
	var allUpdates []Update
	managerDetected := false
	checkFailed := false

	// Try each package manager and track if any were found
	aptResult := getAptUpdates()
	if aptResult.ManagerDetected {
		managerDetected = true
		checkFailed = checkFailed || aptResult.CheckFailed
		allUpdates = append(allUpdates, aptResult.Updates...)
	}

	dnfResult := getDnfUpdates()
	if dnfResult.ManagerDetected {
		managerDetected = true
		checkFailed = checkFailed || dnfResult.CheckFailed
		allUpdates = append(allUpdates, dnfResult.Updates...)
	}

	yumResult := getYumUpdates()
	if yumResult.ManagerDetected {
		managerDetected = true
		checkFailed = checkFailed || yumResult.CheckFailed
		allUpdates = append(allUpdates, yumResult.Updates...)
	}

	zypperResult := getZypperUpdates()
	if zypperResult.ManagerDetected {
		managerDetected = true
		checkFailed = checkFailed || zypperResult.CheckFailed
		allUpdates = append(allUpdates, zypperResult.Updates...)
	}

	nixosResult := getNixosUpdates()
	if nixosResult.ManagerDetected {
		managerDetected = true
		checkFailed = checkFailed || nixosResult.CheckFailed
		allUpdates = append(allUpdates, nixosResult.Updates...)
	}

	return UpdateResult{
		Updates:         allUpdates,
		ManagerDetected: managerDetected,
		CheckFailed:     checkFailed,
	}
}
