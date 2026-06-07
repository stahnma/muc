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

	var stream zypperStream
	if err := xml.Unmarshal(out, &stream); err != nil {
		// zypper ran but produced output we can't parse; status unknown.
		slog.Error("Error parsing zypper XML output", "error", err)
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: true,
			CheckFailed:     true,
		}
	}

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

	debugLog("Found zypper updates", "count", len(updates))
	return UpdateResult{
		Updates:         updates,
		ManagerDetected: true,
	}
}
