package updates

import (
	"bufio"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// runDnfCheckUpdate runs dnf check-update with the given arguments and returns
// stdout, or an error if the command fails with a non-100 exit code.
// Exit code 100 means updates are available and is treated as success.
func runDnfCheckUpdate(args ...string) ([]byte, error) {
	cmd := exec.Command("/usr/bin/dnf", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if exitError.ExitCode() == 100 {
				// Exit code 100 means updates are available — not an error
				return out, nil
			}
			slog.Error("dnf check-update failed", "error", err, "exitCode", exitError.ExitCode(), "stderr", stderr.String())
		} else {
			slog.Error("Error running dnf", "error", err, "stderr", stderr.String())
		}
		return nil, err
	}
	return out, nil
}

// getDnfUpdates fetches updates from dnf package manager
func getDnfUpdates() UpdateResult {
	var updates []Update
	if _, err := os.Stat("/usr/bin/dnf"); err != nil {
		debugLog("dnf not found", "path", "/usr/bin/dnf")
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: false,
		}
	}

	debugLog("Checking for dnf updates...")

	// Try with --setopt first, fall back to plain check-update if it fails
	out, err := runDnfCheckUpdate("check-update", "--setopt=skip_if_unavailable=True")
	if err != nil {
		debugLog("Retrying dnf check-update without --setopt flag")
		out, err = runDnfCheckUpdate("check-update")
	}
	if err != nil {
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: true,
		}
	}

	debugLog("Raw DNF output", "output", string(out))
	scanner := bufio.NewScanner(strings.NewReader(string(out)))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		debugLog("Processing line", "line", line)

		// Skip empty lines
		if line == "" {
			debugLog("Skipping empty line")
			continue
		}

		// Skip metadata expiration check lines
		if strings.HasPrefix(line, "Last metadata") {
			debugLog("Skipping metadata check line")
			continue
		}

		// First check if it's a repo status line
		if isRepoStatusLine(line) {
			debugLog("Skipping repository status line: matched repo pattern")
			continue
		}

		fields := strings.Fields(line)
		debugLog("Found fields", "count", len(fields), "fields", fields)

		// A valid package line must have at least 3 fields:
		// package-name    version-release    repository
		if len(fields) >= 3 {
			// Check each field individually first
			debugLog("Validating package name", "name", fields[0])
			if !isValidPackageName(fields[0]) {
				debugLog("Invalid package name format", "name", fields[0])
				continue
			}

			debugLog("Validating version", "version", fields[1])
			if !isValidVersion(fields[1]) {
				debugLog("Invalid version format", "version", fields[1])
				continue
			}

			debugLog("Validating repository", "repository", fields[2])
			if !isValidRepository(fields[2]) {
				debugLog("Invalid repository format", "repository", fields[2])
				continue
			}

			// If we get here, all fields are valid
			debugLog("All fields valid, adding update", "name", fields[0], "version", fields[1], "source", fields[2])
			updates = append(updates, Update{
				Name:    fields[0],
				Version: fields[1],
				Source:  fields[2],
			})
		} else {
			debugLog("Skipping line with insufficient fields", "line", line)
		}
	}

	debugLog("Found DNF updates", "count", len(updates))
	return UpdateResult{
		Updates:         updates,
		ManagerDetected: true,
	}
}

// isRepoStatusLine checks if a line is a repository status line
func isRepoStatusLine(line string) bool {
	lowercaseLine := strings.ToLower(line)

	// Check for any numbers followed by size units (with or without space)
	sizeUnits := []string{"kb", "mb", "gb", "b"}
	for _, unit := range sizeUnits {
		for i := 0; i < len(lowercaseLine)-len(unit); i++ {
			if i > 0 && isDigit(lowercaseLine[i-1]) &&
				strings.HasPrefix(lowercaseLine[i:], unit) {
				return true
			}
		}
	}

	// Common patterns in repo status lines
	repoPatterns := []string{
		"kb/s", "mb/s", "gb/s",
		"rpms", "rpm",
		" kb ", " mb ", " gb ",
		"|",           // Often used in progress bars
		"downloading", // Common in repo updates
		"metadata",    // Usually part of status messages
	}

	// Check for common repo status patterns
	for _, pattern := range repoPatterns {
		if strings.Contains(lowercaseLine, pattern) {
			return true
		}
	}

	return false
}

// isDigit returns true if the byte is a decimal digit
func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// isValidPackageName checks if the package name follows common conventions
func isValidPackageName(name string) bool {
	// Package names typically:
	// - Start with a letter or number
	// - Contain only letters, numbers, dots, dashes, underscores
	// - Are not all uppercase (usually not a package name)
	if len(name) == 0 {
		return false
	}

	// Most legitimate package names have at least one lowercase letter
	hasLower := false
	for _, c := range name {
		if c >= 'a' && c <= 'z' {
			hasLower = true
			break
		}
	}
	if !hasLower {
		return false
	}

	// Check first character
	first := name[0]
	if !((first >= 'a' && first <= 'z') ||
		(first >= 'A' && first <= 'Z') ||
		(first >= '0' && first <= '9')) {
		return false
	}

	// Check remaining characters
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_') {
			return false
		}
	}

	return true
}

// isValidVersion checks if the version string looks like a real version number
func isValidVersion(version string) bool {
	// Version must contain at least one dot or dash
	if !strings.Contains(version, ".") && !strings.Contains(version, "-") {
		return false
	}

	// Version should not be all uppercase
	if strings.ToUpper(version) == version && strings.ToLower(version) != version {
		return false
	}

	// Version should not contain certain keywords
	lowercaseVersion := strings.ToLower(version)
	invalidKeywords := []string{"rpm", "rpms", "downloading", "metadata"}
	for _, keyword := range invalidKeywords {
		if strings.Contains(lowercaseVersion, keyword) {
			return false
		}
	}

	return true
}

// isValidRepository checks if the repository field looks legitimate
func isValidRepository(repo string) bool {
	// Repository should not be a pure number
	if _, err := strconv.ParseFloat(repo, 64); err == nil {
		return false
	}

	// Repository should not be all uppercase
	if strings.ToUpper(repo) == repo && strings.ToLower(repo) != repo {
		return false
	}

	// Repository should not contain certain keywords
	lowercaseRepo := strings.ToLower(repo)
	invalidKeywords := []string{"rpm", "rpms", "downloading", "metadata"}
	for _, keyword := range invalidKeywords {
		if strings.Contains(lowercaseRepo, keyword) {
			return false
		}
	}

	return true
}
