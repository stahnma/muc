package updates

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"unicode"
)

// containsVersionPattern checks if a string looks like a version number
// Versions typically start with a digit and contain dots, numbers, or plus signs
func containsVersionPattern(s string) bool {
	if len(s) == 0 {
		return false
	}
	// Version should start with a digit
	if !unicode.IsDigit(rune(s[0])) {
		return false
	}
	// Version should contain at least one dot, digit after first char, or plus sign
	hasDot := strings.Contains(s, ".")
	hasPlus := strings.Contains(s, "+")
	hasDigit := false
	for _, r := range s[1:] {
		if unicode.IsDigit(r) {
			hasDigit = true
			break
		}
	}
	return hasDot || hasPlus || hasDigit
}

// hasRootOrSudo checks if the process is running as root or has sudo access
func hasRootOrSudo() (bool, bool) {
	// Check if running as root (UID 0)
	isRoot := os.Getuid() == 0

	// Check if sudo is available and can be used (passwordless)
	hasSudo := false
	if _, err := exec.LookPath("sudo"); err == nil {
		// Try to run sudo -n true (non-interactive, just check if it works)
		cmd := exec.Command("sudo", "-n", "true")
		if err := cmd.Run(); err == nil {
			hasSudo = true
		}
	}

	return isRoot, hasSudo
}

// getNixosUpdates fetches updates from nix package manager
func getNixosUpdates() UpdateResult {
	var updates []Update

	// Check if we're on NixOS by looking for characteristic files/directories
	isNixos := false
	if _, err := os.Stat("/etc/nixos"); err == nil {
		isNixos = true
		debugLog("NixOS detected via /etc/nixos directory")
	} else if _, err := os.Stat("/run/current-system"); err == nil {
		isNixos = true
		debugLog("NixOS detected via /run/current-system")
	}

	if !isNixos {
		debugLog("NixOS not detected")
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: false,
		}
	}

	// Check for root or sudo access
	isRoot, hasSudo := hasRootOrSudo()
	if !isRoot && !hasSudo {
		debugLog("NixOS detected but no root/sudo access available")
		return UpdateResult{
			Updates:         updates,
			ManagerDetected: false,
		}
	}

	debugLog("NixOS detected with appropriate privileges", "is_root", isRoot, "has_sudo", hasSudo)

	// First, update channels
	debugLog("Updating nix channels...")
	var updateCmd *exec.Cmd
	if !isRoot && hasSudo {
		updateCmd = exec.Command("sudo", "nix-channel", "--update")
	} else {
		updateCmd = exec.Command("nix-channel", "--update")
	}

	if err := updateCmd.Run(); err != nil {
		debugLog("Error updating nix channels", "error", err)
		// Continue anyway - channels might already be up to date
	}

	// Run nixos-rebuild dry-run to see what would be updated
	debugLog("Running nixos-rebuild dry-run...")
	var rebuildCmd *exec.Cmd
	if !isRoot && hasSudo {
		rebuildCmd = exec.Command("sudo", "nixos-rebuild", "dry-run")
	} else {
		rebuildCmd = exec.Command("nixos-rebuild", "dry-run")
	}

	// Use CombinedOutput to capture both stdout and stderr
	// nixos-rebuild may write important info to stderr
	out, err := rebuildCmd.CombinedOutput()
	if err != nil {
		debugLog("nixos-rebuild dry-run returned error", "error", err, "output_length", len(out))
		// Exit code might be non-zero even if command works, but log it
		if len(out) == 0 {
			debugLog("Error running nixos-rebuild dry-run with no output", "error", err)
			// NixOS detected but the rebuild produced nothing usable; status unknown.
			return UpdateResult{
				Updates:         updates,
				ManagerDetected: true,
				CheckFailed:     true,
			}
		}
		// Continue parsing even if there was an error code
	}

	// Log raw output for debugging (truncated if too long)
	if len(out) > 0 {
		maxLen := 2000
		if len(out) < maxLen {
			maxLen = len(out)
		}
		debugLog("Raw nixos-rebuild output", "max_len", maxLen, "total_len", len(out), "output", string(out[:maxLen]))
	}

	// Parse the output from nixos-rebuild dry-run
	// The output format is:
	//   "these X paths will be fetched:"
	//   /nix/store/<hash>-<package-name>-<version>
	//   /nix/store/<hash>-<package-name>-<version>
	//   ...
	debugLog("Parsing nixos-rebuild output", "output_length", len(out))
	scanner := bufio.NewScanner(strings.NewReader(string(out)))

	inPathsSection := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		debugLog("Processing nixos-rebuild line", "line", line)

		// Look for the section header "these X paths will be fetched"
		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "paths will be fetched") {
			inPathsSection = true
			debugLog("Entered 'paths will be fetched' section")
			continue
		}

		// If we're in the paths section, parse store paths
		if inPathsSection && strings.HasPrefix(line, "/nix/store/") {
			// Format: /nix/store/<hash>-<package-name>-<version>
			// Hash is always 32 characters (hex)
			// Example: /nix/store/vywr2h9s4vgpnda1q7x0i1is0jcd3c8j-autofs-5.1.9
			//          /nix/store/4avybf0fh7bf2cxs2cjvd4h3g4krc9ck-bind-9.20.16-dnsutils

			// Extract the part after /nix/store/
			storePath := strings.TrimPrefix(line, "/nix/store/")
			if storePath == line {
				// Didn't start with /nix/store/, skip
				continue
			}

			// Split by dashes - first part is hash (32 chars), rest is package+version
			dashParts := strings.Split(storePath, "-")
			if len(dashParts) < 2 {
				debugLog("Skipping invalid store path format", "path", storePath)
				continue
			}

			// Skip the hash (first part), rest is package identifier
			// Examples after removing hash:
			//   ["autofs", "5.1.9"] -> package="autofs", version="5.1.9"
			//   ["bind", "9.20.16", "dnsutils"] -> package="bind-9.20.16-dnsutils", version=""
			//   ["linux", "6.12.62"] -> package="linux", version="6.12.62"
			//   ["nix", "2.31.2+1"] -> package="nix", version="2.31.2+1"
			packageParts := dashParts[1:]

			if len(packageParts) == 0 {
				continue
			}

			packageName := ""
			version := ""

			// Check if the last part looks like a version (starts with digit)
			lastPart := packageParts[len(packageParts)-1]
			if containsVersionPattern(lastPart) && len(packageParts) >= 2 {
				// Last part is a version, everything before is the package name
				packageName = strings.Join(packageParts[:len(packageParts)-1], "-")
				version = lastPart
			} else {
				// Either single part or last part doesn't look like a version
				// Treat the entire thing as package name
				packageName = strings.Join(packageParts, "-")
				version = ""
			}

			if packageName != "" {
				debugLog("Parsed nixos package update", "package", packageName, "version", version, "raw_path", storePath)
				updates = append(updates, Update{
					Name:    packageName,
					Version: version,
					Source:  "nixos",
				})
			}
		} else if inPathsSection {
			// If we're in paths section but hit a line that doesn't start with /nix/store/,
			// check if we've left the paths section
			// Look for indicators that we've moved to a new section
			trimmedLine := strings.TrimSpace(line)
			if trimmedLine != "" {
				lowerTrimmed := strings.ToLower(trimmedLine)
				// Check if this looks like a new section header (e.g., "these X derivations will be built:")
				if strings.Contains(lowerTrimmed, "will be") && !strings.Contains(lowerTrimmed, "paths will be fetched") {
					// This is a different section, exit paths section
					inPathsSection = false
					debugLog("Left 'paths will be fetched' section, found new section", "line", trimmedLine)
				}
				// Otherwise, might be a continuation or empty line, keep going
			}
		}
	}

	debugLog("Found nixos updates", "count", len(updates))
	return UpdateResult{
		Updates:         updates,
		ManagerDetected: true,
	}
}
