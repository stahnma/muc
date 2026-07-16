package updates

import (
	"encoding/xml"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// zypperStream models the --xmlout output of `zypper list-updates`.
//
// Example:
//
//	<stream>
//	  <update-status version="0.6">
//	    <update-list>
//	      <update name="bash" edition="5.2-3.1" arch="x86_64" kind="package">
//	        <source url="http://.../oss" alias="repo-oss"/>
//	      </update>
//	    </update-list>
//	  </update-status>
//	</stream>
type zypperStream struct {
	Updates []zypperUpdate `xml:"update-status>update-list>update"`
}

type zypperUpdate struct {
	Name    string `xml:"name,attr"`
	Edition string `xml:"edition,attr"`
	Kind    string `xml:"kind,attr"`
	Source  struct {
		Alias string `xml:"alias,attr"`
	} `xml:"source"`
}

// getZypperUpdates fetches updates from the zypper package manager (openSUSE,
// SUSE Linux Enterprise). It parses zypper's XML output, which is stable across
// versions and locales — unlike the human-readable table.
func getZypperUpdates() UpdateResult {
	var updates []Update

	if _, err := os.Stat("/usr/bin/zypper"); err != nil {
		debugLog("zypper not found", "path", "/usr/bin/zypper")
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: false,
		}
	}

	debugLog("Checking for zypper updates...")

	// zypper computes list-updates against its *cached* repository metadata and
	// only refreshes that cache when invoked as root. Unlike Fedora
	// (dnf-makecache.timer) or Debian (apt-daily.timer), openSUSE ships no
	// periodic refresh, so a cold or stale cache makes list-updates silently
	// under-report — typically as zero updates on a machine that is hundreds of
	// updates behind. Force a refresh when we can. refreshed records whether we
	// managed to; it gates how we interpret an empty result below.
	refreshed := refreshZypperMetadata()

	// --non-interactive avoids any prompts; --xmlout gives machine-readable
	// output. zypper exits 0 even when updates are available, so any non-zero
	// exit is a genuine failure to query — report unknown, not "up to date".
	cmd := exec.Command("/usr/bin/zypper", "--non-interactive", "--xmlout", "list-updates")
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			slog.Error("zypper list-updates failed", "error", err, "exitCode", exitError.ExitCode(), "stderr", stderr.String())
		} else {
			slog.Error("Error running zypper", "error", err, "stderr", stderr.String())
		}
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: true,
			CheckFailed:     true,
		}
	}

	debugLog("Raw zypper output", "output", string(out))

	updates, err = parseZypperUpdates(out)
	if err != nil {
		// zypper ran but produced output we can't parse; status unknown.
		slog.Error("Error parsing zypper XML output", "error", err)
		return UpdateResult{
			Updates:         nil,
			ManagerDetected: true,
			CheckFailed:     true,
		}
	}

	// If we could not refresh the metadata (running unprivileged, or the refresh
	// failed) and the check came back empty, we cannot tell "genuinely up to
	// date" from "stale cache we were unable to update" — report unknown rather
	// than a confident "up to date". A non-empty result is trustworthy either
	// way: those updates really are pending.
	if len(updates) == 0 && !refreshed {
		slog.Warn("zypper reported no updates but metadata could not be refreshed; reporting unknown", "euid", os.Geteuid())
		return UpdateResult{
			Updates:         nil,
			ManagerDetected: true,
			CheckFailed:     true,
		}
	}

	debugLog("Found zypper updates", "count", len(updates))
	return UpdateResult{
		Updates:         updates,
		ManagerDetected: true,
	}
}

// parseZypperUpdates turns the --xmlout output of `zypper list-updates` into the
// list of pending package updates, dropping non-package kinds (patches,
// patterns, products) to match what the other package managers report.
func parseZypperUpdates(out []byte) ([]Update, error) {
	var stream zypperStream
	if err := xml.Unmarshal(out, &stream); err != nil {
		return nil, err
	}

	var updates []Update
	for _, u := range stream.Updates {
		// list-updates only reports packages by default, but newer zypper can
		// include patches/patterns; keep package updates to match other managers.
		if u.Kind != "" && u.Kind != "package" {
			debugLog("Skipping non-package update", "name", u.Name, "kind", u.Kind)
			continue
		}
		updates = append(updates, Update{
			Name:    u.Name,
			Version: u.Edition,
			Source:  u.Source.Alias,
		})
	}
	return updates, nil
}

// refreshZypperMetadata refreshes zypper's cached repository metadata so a
// subsequent list-updates reflects the current repos. This requires root; when
// running unprivileged we skip it and return false. The refresh is best-effort —
// a failure (network, repo error, lock) is logged but not fatal, and callers
// treat "not refreshed" as a reason to distrust an empty update list.
func refreshZypperMetadata() bool {
	if os.Geteuid() != 0 {
		debugLog("Skipping zypper refresh: not running as root")
		return false
	}

	cmd := exec.Command("/usr/bin/zypper", "--non-interactive", "refresh")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("zypper refresh failed; update check may use stale metadata", "error", err, "stderr", stderr.String())
		return false
	}

	debugLog("Refreshed zypper metadata")
	return true
}
