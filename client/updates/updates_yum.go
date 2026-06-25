package updates

import (
	"bufio"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// getYumUpdates fetches updates from yum package manager
func getYumUpdates() UpdateResult {
	var updates []Update

	// If DNF exists, skip YUM check since DNF is the successor
	if _, err := os.Stat("/usr/bin/dnf"); err == nil {
		debugLog("DNF detected, skipping YUM update check")
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: false,
		}
	}

	// Check if yum exists
	if _, err := os.Stat("/usr/bin/yum"); err != nil {
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: false,
		}
	}

	// Force a metadata refresh when the cache is stale (see dnfMetadataExpire)
	// so we don't under-report freshly-published updates from cached metadata.
	out, err := exec.Command("/usr/bin/yum", "check-update",
		"--setopt=metadata_expire="+dnfMetadataExpire).Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if exitError.ExitCode() != 100 {
				// yum exists but check-update failed; status unknown, not "up to date".
				slog.Error("Error checking updates with yum", "error", err, "exitCode", exitError.ExitCode())
				return UpdateResult{
					Updates:         updates,
					ManagerDetected: true,
					CheckFailed:     true,
				}
			}
		} else {
			slog.Error("Error running yum", "error", err)
			return UpdateResult{
				Updates:         updates,
				ManagerDetected: true,
				CheckFailed:     true,
			}
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 3 && !strings.HasPrefix(line, "Loaded plugins") {
			updates = append(updates, Update{
				Name:    fields[0],
				Version: fields[1],
				Source:  fields[2],
			})
		}
	}
	return UpdateResult{
		Updates:         updates,
		ManagerDetected: true,
	}
}
