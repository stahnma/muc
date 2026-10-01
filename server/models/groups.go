package models

import (
	"fmt"
	"strings"
	"unicode"
)

// MaxGroupNameLength bounds a group name. Nothing technical needs the limit —
// it is there so a pasted accident cannot become a chip nobody can read or a
// bbolt key nobody can find again.
const MaxGroupNameLength = 64

// Group is a named set of hosts, managed on the server rather than declared by
// the hosts themselves. Nothing has to be configured on a host to put it in a
// group, and a host cannot put itself in one.
//
// Membership lives in its own bucket rather than on System because a client
// publishes a whole System on every check-in and the server stores what it
// sent. Anything server-owned kept there has to be carried forward by hand on
// every check-in — there are already three such fields — and the next person to
// add one would have no reason to suspect groups were among them.
//
// Members may name a host the server has never seen. That is deliberate: a host
// deleted from the dashboard and re-added should not silently lose its groups,
// and a group can be built before the hosts in it are provisioned. The
// dashboard shows such a member greyed out, and a group action skips it.
type Group struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
	// Derived marks a group whose membership is computed from what hosts
	// report rather than stored. It is never true for anything in the groups
	// bucket, and a derived group cannot be created, renamed, deleted, or
	// have its membership edited. See derived.go.
	Derived bool `json:"derived,omitempty"`
}

// ValidateGroupName reports why a name cannot be used, or nil if it can. The
// name is assumed already trimmed by NormalizeGroupName.
//
// A group name is a URL path segment and a bbolt key, so it rules out the
// separator and anything unprintable; the rest is just keeping it legible.
func ValidateGroupName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a group name is required")
	case len(name) > MaxGroupNameLength:
		return fmt.Errorf("a group name may be at most %d characters", MaxGroupNameLength)
	case strings.ContainsRune(name, '/'):
		return fmt.Errorf("a group name may not contain %q", "/")
	case strings.ContainsRune(name, ':'):
		// ":" is how a derived group is told from a stored one. Reserving it
		// means a hand-made group can never shadow "os:fedora" or be shadowed
		// by it, which would otherwise make a group action ambiguous about
		// which set of hosts it was acting on.
		return fmt.Errorf("a group name may not contain %q, which is reserved for derived groups like %q", ":", "os:fedora")
	}

	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("a group name may not contain control characters")
		}
	}

	return nil
}

// NormalizeGroupName trims the surrounding whitespace a text input collects.
// Case is left alone: a group is stored as it was typed, and only compared
// case-insensitively (see SameGroupName).
func NormalizeGroupName(name string) string {
	return strings.TrimSpace(name)
}

// SameGroupName reports whether two names refer to the same group.
//
// Comparing case-insensitively is what stops "prod" and "Prod" from both
// existing. That pair is the one mistake worth designing against here: both
// chips look right, and a group action on either one silently misses half the
// fleet rather than failing.
func SameGroupName(a, b string) bool {
	return strings.EqualFold(a, b)
}
