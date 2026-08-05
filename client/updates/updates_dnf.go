package updates

import (
	"bufio"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// dnfMetadataExpire bounds how stale dnf's cached repo metadata may be before a
// check-update forces a re-download. The client polls every few minutes, so we
// must NOT refresh on every poll (that would hammer mirrors hundreds of times a
// day). Instead we shorten dnf's metadata_expire: dnf re-syncs only when the
// cache is older than this, otherwise it answers from cache for free.
//
// Without this, the client inherits each repo's default expiry (6h+ for Fedora's
// "updates" repo, days for others), so a freshly-published update stays invisible
// on the dashboard until the cache happens to expire — the symptom that surfaced
// as "dashboard shows 2 updates, `dnf update` shows 3".
const dnfMetadataExpire = "1h"

// dnfMetadataExpireSetopt is the --setopt that actually shortens the expiry.
//
// The "*." repoid glob is load-bearing. A bare `--setopt=metadata_expire=1h`
// only writes dnf's [main] section, and a per-repo `metadata_expire` in
// /etc/yum.repos.d/*.repo overrides [main] — which RHEL/Rocky/CentOS all set to
// 6h in their stock rocky.repo/redhat.repo. So the unprefixed form is a silent
// no-op on exactly the repos that matter most. Verified on Rocky 10:
//
//	--setopt=metadata_expire=1h     -> metadata_expire = 21600  (6h, unchanged)
//	--setopt=*.metadata_expire=1h   -> metadata_expire = 3600   (1h, applied)
const dnfMetadataExpireSetopt = "--setopt=*.metadata_expire=" + dnfMetadataExpire

// dnfCacheDir returns the directory dnf should use for its metadata cache, or
// "" to leave dnf's own default alone.
//
// Running as root we return "" so dnf uses /var/cache/dnf and shares the cache
// that dnf-makecache.timer (and the admin's own dnf runs) keep warm.
//
// Running unprivileged dnf cannot write /var/cache/dnf, so it falls back to a
// throwaway /var/tmp/dnf-$USER-XXXX directory. Under our systemd unit that is
// doubly bad: PrivateTmp=true gives the service a private /var/tmp that is
// destroyed on every restart, and with Restart=always the client re-downloads
// tens of megabytes of repo metadata each time it comes back. Point dnf at the
// service's StateDirectory instead so the cache actually persists.
func dnfCacheDir() string {
	if os.Geteuid() == 0 {
		return ""
	}

	// systemd sets STATE_DIRECTORY from StateDirectory= (/var/lib/muc). It is
	// colon-separated when several are configured; the first is ours.
	if state := os.Getenv("STATE_DIRECTORY"); state != "" {
		first, _, _ := strings.Cut(state, ":")
		if first != "" {
			return filepath.Join(first, "dnf")
		}
	}

	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".cache", "muc-dnf")
	}
	return ""
}

// dnfCheckUpdateArgs returns the arguments for the primary dnf check-update
// invocation: the short metadata_expire that keeps results fresh, a persistent
// cache directory, and -y to accept repository signing keys unattended.
//
// On -y: a repo with repo_gpgcheck=1 verifies repomd.xml against a per-repo
// keyring under the cache dir, separate from the system rpm keyring. When that
// keyring lacks the key dnf wants to import it and prompts; unattended the
// prompt is declined, skip_if_unavailable drops the repo, and its updates go
// unreported. -y accepts instead, which is what makes the repo visible at all.
//
// The trade is real and deliberate: the client will trust whatever key the
// repo's configured gpgkey= URL serves, so a hijacked gpgkey URL or a
// compromised repo host would be accepted rather than flagged. check-update
// installs nothing, so the blast radius is confined to which keys this host's
// muc cache trusts for metadata verification — not to package installation,
// which still verifies against the root-owned rpm keyring. Every import is
// logged (see parseDnfImportedKeys) so the decision is auditable after the fact.
func dnfCheckUpdateArgs() []string {
	args := []string{
		"check-update",
		"-y",
		"--setopt=skip_if_unavailable=True",
		dnfMetadataExpireSetopt,
	}
	if dir := ensureDnfCacheDir(); dir != "" {
		args = append(args, "--setopt=cachedir="+dir)
	}
	return args
}

// ensureDnfCacheDir creates the cache directory from dnfCacheDir if needed. A
// failure is not fatal: we return "" and let dnf pick its own location rather
// than failing the whole update check.
func ensureDnfCacheDir() string {
	dir := dnfCacheDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		slog.Warn("Could not create dnf cache directory; falling back to dnf's default (cache will not persist across restarts)",
			"dir", dir, "error", err)
		return ""
	}
	return dir
}

// dnfSkippedRepoPatterns match the stderr dnf4 emits when skip_if_unavailable
// swallows a repository failure. dnf still exits 0/100, so without scraping
// these the client reports a silently incomplete package list as authoritative.
var dnfSkippedRepoPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^Ignoring repositories:\s*(.+)$`),
	regexp.MustCompile(`Failed to download metadata for repo '([^']+)'`),
	regexp.MustCompile(`Errors? during downloading metadata for repository '([^']+)'`),
}

// dnf5DiagnosticPattern matches how dnf5 (Fedora 41+) reports repository
// trouble: a ">>> " prefixed line on stderr. None of the dnf4 phrasings above
// appear, so a dnf5 host was silently reporting an incomplete package list as a
// confident "up to date" until these were handled.
//
// dnf5 does not name the offending repository in these messages — unlike dnf4,
// which quotes the repoid — so the descriptor falls back to the server hostname
// from the URL, or to the message itself. Less precise than a repoid, but it
// still tells an operator the count is incomplete and why.
var dnf5DiagnosticPattern = regexp.MustCompile(`(?m)^>>>\s*(.+?)\s*$`)

// dnf5URLPattern pulls the host out of a dnf5 diagnostic so the warning can name
// something recognisable rather than a bare error string.
var dnf5URLPattern = regexp.MustCompile(`https?://([^/\s\]]+)`)

const (
	// dnf5KeyMissing is emitted before dnf5 offers to import a repo signing key.
	// It appears even when the import then succeeds and the repo loads fine, so
	// on its own it does NOT mean the repo was skipped.
	dnf5KeyMissing = "Signing key not found"
	// dnf5KeyImported is dnf5's confirmation that the import above went through,
	// which retroactively makes the preceding dnf5KeyMissing diagnostic benign.
	dnf5KeyImported = "The key was successfully imported"
)

// parseDnfSkippedRepos extracts the repositories dnf dropped because
// skip_if_unavailable turned a metadata failure into a warning. Their packages
// are missing from the result even though the command exited cleanly, so the
// count is a lower bound rather than an answer.
//
// The common unprivileged trigger is repo_gpgcheck=1: verifying repomd.xml uses
// a per-user GPG directory under the cache dir, not the system rpm keyring, so
// dnf tries to import the key, cannot prompt, and drops the repo.
func parseDnfSkippedRepos(stderr string) []string {
	seen := make(map[string]bool)
	var repos []string

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		repos = append(repos, name)
	}

	for _, re := range dnfSkippedRepoPatterns {
		for _, m := range re.FindAllStringSubmatch(stderr, -1) {
			// "Ignoring repositories:" lists several, comma- or space-separated.
			for _, field := range strings.FieldsFunc(m[1], func(r rune) bool {
				return r == ',' || r == ' ' || r == '\t'
			}) {
				add(field)
			}
		}
	}

	// dnf5 speaks differently; see dnf5DiagnosticPattern.
	keyImported := strings.Contains(stderr, dnf5KeyImported)
	for _, m := range dnf5DiagnosticPattern.FindAllStringSubmatch(stderr, -1) {
		msg := m[1]
		// A missing signing key that dnf5 then imported successfully is not a
		// skipped repo — the repo loads. Without this every host's first contact
		// with a new repo would raise a false warning.
		if strings.Contains(msg, dnf5KeyMissing) && keyImported {
			continue
		}
		add(dnf5RepoDescriptor(msg))
	}

	sort.Strings(repos)
	return repos
}

// dnf5RepoDescriptor turns a dnf5 diagnostic into the most identifiable label
// available: the server hostname when the message carries a URL, otherwise the
// message itself. dnf5 never includes the repoid, so this is the best handle an
// operator gets from stderr alone.
func dnf5RepoDescriptor(msg string) string {
	if m := dnf5URLPattern.FindStringSubmatch(msg); m != nil {
		return m[1] + " (" + dnf5Reason(msg) + ")"
	}
	return dnf5Reason(msg)
}

// dnf5Reason trims a diagnostic down to its cause, dropping the repeated URL
// and bracketed curl detail that would otherwise make the warning unreadable on
// the dashboard.
func dnf5Reason(msg string) string {
	if i := strings.Index(msg, " for http"); i > 0 {
		msg = msg[:i]
	}
	if i := strings.Index(msg, " ["); i > 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(msg), ":"))
}

// dnfPath is where the client looks for dnf. Also used as the "is this a dnf
// host" probe, and by updates_yum.go to decide whether yum has been superseded.
const dnfPath = "/usr/bin/dnf"

// dnfFreshnessNotEnforced is recorded in SkippedRepos when every attempt to pin
// metadata_expire failed, so the result came from whatever each repo's own
// (typically 6h+) expiry left in cache.
const dnfFreshnessNotEnforced = "(metadata freshness not enforced)"

// dnfRunFunc is the shape of a check-update invocation: stdout, stderr, error.
type dnfRunFunc func(args ...string) (stdout []byte, stderrOut string, err error)

// dnfRunner is the seam tests replace to drive collectDnfUpdates without a real
// dnf. Production always uses runDnfCheckUpdate.
var dnfRunner dnfRunFunc = runDnfCheckUpdate

// dnfImportedKeyPattern matches dnf's announcement that it is adopting a
// repository signing key. Because the client passes -y, that adoption happens
// without anyone confirming it, so it is logged to leave an audit trail.
//
// dnf4 says "Importing GPG key 0x...", dnf5 says "Importing OpenPGP key 0x...".
// Matching only dnf4's wording left the audit trail empty on Fedora hosts —
// precisely where keys were being adopted silently.
var dnfImportedKeyPattern = regexp.MustCompile(`Importing (?:GPG|OpenPGP) key (0x[0-9A-Fa-f]+)`)

// parseDnfImportedKeys extracts the key IDs dnf imported during a check. An
// import is normal on a host's first sight of a repo and should be rare after
// that; a key appearing for a repo that already worked is worth investigating,
// which is only possible if it was recorded.
func parseDnfImportedKeys(stderr string) []string {
	var keys []string
	seen := make(map[string]bool)
	for _, m := range dnfImportedKeyPattern.FindAllStringSubmatch(stderr, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			keys = append(keys, m[1])
		}
	}
	return keys
}

// runDnfCheckUpdate runs dnf check-update with the given arguments and returns
// stdout plus stderr, or an error if the command fails with a non-100 exit code.
// Exit code 100 means updates are available and is treated as success.
//
// stderr is returned even on success because that is where dnf reports the
// repositories it silently skipped; see parseDnfSkippedRepos.
func runDnfCheckUpdate(args ...string) (stdout []byte, stderrOut string, err error) {
	cmd := exec.Command(dnfPath, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if exitError.ExitCode() == 100 {
				// Exit code 100 means updates are available — not an error
				return out, stderr.String(), nil
			}
			slog.Error("dnf check-update failed", "error", err, "exitCode", exitError.ExitCode(), "stderr", stderr.String())
		} else {
			slog.Error("Error running dnf", "error", err, "stderr", stderr.String())
		}
		return nil, stderr.String(), err
	}
	return out, stderr.String(), nil
}

// getDnfUpdates fetches updates from dnf package manager
func getDnfUpdates() UpdateResult {
	if _, err := os.Stat(dnfPath); err != nil {
		debugLog("dnf not found", "path", dnfPath)
		return UpdateResult{ManagerDetected: false}
	}

	debugLog("Checking for dnf updates...")
	return collectDnfUpdates(dnfRunner)
}

// collectDnfUpdates drives the check-update invocation and turns it into a
// result. It takes the runner as a parameter so the retry ladder and the
// degraded/skipped bookkeeping can be exercised without a real dnf on the box.
func collectDnfUpdates(run dnfRunFunc) UpdateResult {
	// Try the full argument set first, then degrade one step at a time. Each
	// fallback keeps the freshness flags (metadata_expire, cachedir) for as long
	// as possible: dropping them silently reverts to each repo's own 6h+ expiry
	// and a throwaway cache, which is the staleness this whole path exists to
	// avoid. Only the last resort runs a bare check-update, and that run is
	// recorded as degraded so an empty result is not trusted.
	out, stderr, err := run(dnfCheckUpdateArgs()...)
	degraded := false
	if err != nil {
		debugLog("Retrying dnf check-update without skip_if_unavailable")
		retry := []string{"check-update", "-y", dnfMetadataExpireSetopt}
		if dir := ensureDnfCacheDir(); dir != "" {
			retry = append(retry, "--setopt=cachedir="+dir)
		}
		out, stderr, err = run(retry...)
	}
	if err != nil {
		debugLog("Retrying bare dnf check-update; results may come from stale metadata")
		degraded = true
		out, stderr, err = run("check-update")
	}
	if err != nil {
		// dnf is installed but check-update failed to run (e.g. read-only
		// cache/log dir under a hardened sandbox). Report unknown, not "up to date".
		return UpdateResult{
			ManagerDetected: true,
			CheckFailed:     true,
		}
	}

	// The client passes -y, so any repository signing key dnf decided to adopt
	// was adopted without confirmation. Record it: this is the only trace that
	// the host's trusted set changed.
	if keys := parseDnfImportedKeys(stderr); len(keys) > 0 {
		slog.Warn("dnf imported repository signing keys unattended (-y); the set of keys trusted for metadata verification has changed",
			"keys", keys, "euid", os.Geteuid())
	}

	// skip_if_unavailable turns an unreachable or unverifiable repo into a
	// warning on stderr and a clean exit, so the package list silently omits
	// everything that repo would have contributed.
	skipped := parseDnfSkippedRepos(stderr)
	if len(skipped) > 0 {
		slog.Warn("dnf skipped repositories; the update list is incomplete",
			"repos", skipped, "euid", os.Geteuid())
	}
	if degraded {
		// We could not pin metadata_expire, so dnf answered from whatever its
		// repos' own (typically 6h+) expiry left in cache. Flag it the same way
		// as a skipped repo: the result is a lower bound, not an answer.
		skipped = append(skipped, dnfFreshnessNotEnforced)
	}

	debugLog("Raw DNF output", "output", string(out))
	updates := parseDnfUpdates(out)

	debugLog("Found DNF updates", "count", len(updates), "skippedRepos", skipped)
	return UpdateResult{
		Updates:         updates,
		ManagerDetected: true,
		SkippedRepos:    skipped,
	}
}

// parseDnfUpdates turns the stdout of `dnf check-update` into the list of
// pending package updates.
//
// dnf interleaves progress, mirror status and metadata notices with the package
// table on stdout, and there is no machine-readable mode we can rely on across
// dnf4 and dnf5, so each line is validated field by field before it is accepted
// as a package. The validators are deliberately conservative: a missed package
// under-reports, but a false positive puts a nonexistent update on the dashboard.
func parseDnfUpdates(out []byte) []Update {
	var updates []Update
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
		if len(fields) < 3 {
			debugLog("Skipping line with insufficient fields", "line", line)
			continue
		}

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
	}

	return updates
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
