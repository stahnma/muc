package updates

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// getAptUpdates fetches updates from apt package manager
func getAptUpdates() UpdateResult {
	var updates []Update
	if _, err := os.Stat("/usr/bin/apt"); err != nil {
		debugLog("apt not found at /usr/bin/apt")
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: false,
		}
	}

	debugLog("Checking for apt updates...")
	out, err := exec.Command("/usr/bin/apt", "list", "--upgradable").Output()
	if err != nil {
		slog.Error("Error checking updates with apt", "error", err)
		// apt exists but the check failed; we don't actually know the status.
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: true,
			CheckFailed:     true,
		}
	}

	// Log raw output for debugging
	if len(out) > 0 {
		maxLen := 500
		if len(out) < maxLen {
			maxLen = len(out)
		}
		debugLog("Raw apt output", "max_len", maxLen, "output", string(out[:maxLen]))
	}

	lines := strings.Split(string(out), "\n")
	for lineNum, line := range lines {
		// Skip header lines and empty lines
		if strings.HasPrefix(line, "Listing") || strings.TrimSpace(line) == "" {
			continue
		}

		// Log raw line before any processing
		debugLog("Processing apt line", "line_num", lineNum+1, "line", line)

		fields := strings.Fields(line)
		if len(fields) < 2 {
			debugLog("Skipping line: insufficient fields", "line_num", lineNum+1, "field_count", len(fields))
			continue
		}

		debugLog("Split into fields", "line_num", lineNum+1, "field_count", len(fields), "fields", fields)

		// Split package name into name and source
		// fields[0] format should be: "package-name/repository-name" or "package-name/repo1,repo2"
		// If apt outputs spaces instead of commas, fields[0] might only contain first repo
		parts := strings.Split(fields[0], "/")
		debugLog("Split package field", "line_num", lineNum+1, "field", fields[0], "parts", parts)

		if len(parts) < 2 {
			debugLog("Skipping line: no '/' separator", "line_num", lineNum+1, "field", fields[0])
			continue
		}

		packageName := parts[0]
		rawSource := parts[1] // This contains whatever apt outputs after the "/"

		debugLog("Extracted package info", "line_num", lineNum+1, "package", packageName, "raw_source", rawSource)

		// Deduplicate source to handle cases where apt outputs duplicates
		source := deduplicateSource(rawSource)
		if rawSource != source {
			debugLog("Deduplicated source", "line_num", lineNum+1, "original", rawSource, "deduplicated", source)
		}

		// The version should be in fields[1] (standard apt output format)
		version := fields[1]

		debugLog("Final package info", "line_num", lineNum+1, "package", packageName, "version", version, "source", source)
		updates = append(updates, Update{
			Name:    packageName,
			Version: version,
			Source:  source,
		})
	}
	debugLog("Found apt updates", "count", len(updates))
	return UpdateResult{
		Updates:         updates,
		ManagerDetected: true,
	}
}

// getAptSource determines the source repository for a package
func getAptSource(packageName string) string {
	debugLog("Getting source for package", "package", packageName)
	out, err := exec.Command("/usr/bin/apt-cache", "policy", packageName).Output()
	if err != nil {
		slog.Error("Error checking repository source for package", "package", packageName, "error", err)
		return "unknown"
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, " 500 ") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				source := fields[len(fields)-1]
				debugLog("Found source for package", "source", source, "package", packageName)
				return source
			}
		}
	}
	debugLog("No source found for package, using 'unknown'", "package", packageName)
	return "unknown"
}

// deduplicateSource removes duplicate entries from comma-separated source strings.
//
// Why duplicates occur:
// APT itself may output duplicate repository names in the format:
//
//	"package-name/repo1,repo2,repo1"
//
// This typically happens when:
//  1. The same repository is listed multiple times in /etc/apt/sources.list or
//     /etc/apt/sources.list.d/* files (e.g., both deb and deb-src entries for same repo)
//  2. Multiple repository configurations reference the same repository with different
//     identifiers but same name
//  3. Architecture-specific packages (especially "all" architecture) may list the
//     same source multiple times
//
// Our parsing correctly extracts what apt outputs, so we deduplicate here to clean
// up the display. This is NOT a parsing bug - it's apt's output that contains duplicates.
//
// Examples:
//
//	"oldoldstable-security,oldoldstable-security" -> "oldoldstable-security"
//	"repo1,repo2,repo1" -> "repo1,repo2"
//	"repo1,repo2" -> "repo1,repo2" (no change if already unique)
func deduplicateSource(source string) string {
	if source == "" {
		return source
	}

	// Split by comma
	sources := strings.Split(source, ",")

	// Use a map to track unique sources and preserve order
	seen := make(map[string]bool)
	var uniqueSources []string

	for _, s := range sources {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			uniqueSources = append(uniqueSources, s)
		}
	}

	// Join back with commas
	return strings.Join(uniqueSources, ",")
}
