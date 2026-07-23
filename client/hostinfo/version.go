package hostinfo

import "strings"

// version comparison implements a subset of rpm's rpmvercmp algorithm, which
// is what tools like `sort -V` approximate. It is sufficient for kernel
// version-release strings, which never carry an epoch and do not use the '~'
// (tilde) or '^' (caret) ordering markers.

// newestVersion returns the greatest version in the list per compareVersions.
func newestVersion(versions []string) string {
	newest := ""
	for _, v := range versions {
		if newest == "" || compareVersions(v, newest) > 0 {
			newest = v
		}
	}
	return newest
}

// compareVersions returns -1, 0, or 1 as a is older than, equal to, or newer
// than b. Both strings are split into alternating runs of digits and letters;
// separators are ignored, numeric runs compare numerically (longer wins after
// stripping leading zeros), and a numeric run outranks a letter run.
func compareVersions(a, b string) int {
	if a == b {
		return 0
	}

	i, j := 0, 0
	for i < len(a) && j < len(b) {
		// Skip separators (anything that is not alphanumeric).
		for i < len(a) && !isAlphaNum(a[i]) {
			i++
		}
		for j < len(b) && !isAlphaNum(b[j]) {
			j++
		}
		if i >= len(a) || j >= len(b) {
			break
		}

		numeric := isDigit(a[i])

		startA := i
		for i < len(a) && sameClass(a[i], numeric) {
			i++
		}
		segA := a[startA:i]

		startB := j
		for j < len(b) && sameClass(b[j], numeric) {
			j++
		}
		segB := b[startB:j]

		// b's run is empty when its current character is the other class than
		// a's run. A numeric run always outranks a letter run.
		if segB == "" {
			if numeric {
				return 1
			}
			return -1
		}

		if numeric {
			segA = strings.TrimLeft(segA, "0")
			segB = strings.TrimLeft(segB, "0")
			if len(segA) != len(segB) {
				if len(segA) > len(segB) {
					return 1
				}
				return -1
			}
		}

		if segA != segB {
			if segA > segB {
				return 1
			}
			return -1
		}
	}

	// All compared segments were equal; whichever string still has segments
	// left is the newer one.
	remainingA := hasAlphaNum(a[i:])
	remainingB := hasAlphaNum(b[j:])
	switch {
	case remainingA && !remainingB:
		return 1
	case !remainingA && remainingB:
		return -1
	default:
		return 0
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isAlphaNum(c byte) bool { return isDigit(c) || isAlpha(c) }

func sameClass(c byte, numeric bool) bool {
	if numeric {
		return isDigit(c)
	}
	return isAlpha(c)
}

func hasAlphaNum(s string) bool {
	for i := 0; i < len(s); i++ {
		if isAlphaNum(s[i]) {
			return true
		}
	}
	return false
}
