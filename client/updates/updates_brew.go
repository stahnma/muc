package updates

import (
	"bufio"
	"log/slog"
	"os/exec"
	"strings"
)

// getBrewUpdates fetches updates from brew package manager
func getBrewUpdates() UpdateResult {
	var updates []Update
	debugLog("Checking for brew updates")

	// Check if brew exists
	if _, err := exec.LookPath("brew"); err != nil {
		debugLog("brew not found in PATH")
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: false,
		}
	}

	// If we get here, brew exists, so mark as detected even if command fails
	out, err := exec.Command("brew", "outdated").Output()
	if err != nil {
		slog.Error("Error checking updates with brew", "error", err)
		// brew exists but the check failed; we don't actually know the status.
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: true,
			CheckFailed:     true,
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		debugLog("Found brew update", "package", line)
		updates = append(updates, Update{
			Name:   line,
			Source: "brew",
		})
	}
	debugLog("Found brew updates", "count", len(updates))
	return UpdateResult{
		Updates:         updates,
		ManagerDetected: true,
	}
}
