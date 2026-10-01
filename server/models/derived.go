package models

import (
	"sort"
	"strings"
)

// Derived groups are computed from what hosts already report, rather than
// curated by hand. "Every Fedora box" and "everything rpm-based" are facts
// about the fleet, not decisions about it, so asking someone to maintain them
// as membership lists would be asking them to keep re-deriving something the
// server can see for itself.
//
// They are namespaced by a prefix so a derived group can never collide with a
// hand-made one; ValidateGroupName rejects ":" in a name for the same reason.
// A derived group exists only while at least one host matches it: boot a
// Debian machine and pkg:deb appears, retire it and the group goes away. There
// is nothing to clean up, which is the whole point.
const (
	DerivedPrefixOS    = "os:"
	DerivedPrefixPkg   = "pkg:"
	DerivedPrefixArch  = "arch:"
	DerivedPrefixState = "state:"
)

// derivedPrefixes is the set used to tell a derived name from a stored one.
var derivedPrefixes = []string{
	DerivedPrefixOS, DerivedPrefixPkg, DerivedPrefixArch, DerivedPrefixState,
}

// IsDerivedName reports whether a name belongs to the derived namespace, and
// therefore names a group that is computed rather than stored. Nothing can
// create, rename, delete or edit the membership of such a group.
func IsDerivedName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range derivedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// osFamilyRule maps a substring of the OS string to a family slug.
//
// The OS string is the distribution's own PRETTY_NAME, so this is a matching
// table rather than an enumeration, and order matters: Pop!_OS has to be
// tested before Ubuntu because it reports "Pop!_OS 22.04 LTS" and is an Ubuntu
// derivative, and the same holds for the RHEL rebuilds.
//
// A distribution not listed here simply joins no os: or pkg: group. That is
// the honest outcome — guessing a package family from an unrecognised name is
// how a host ends up in "all rpm" and gets handed a dnf command it cannot run.
type osFamilyRule struct {
	match  []string
	family string
	pkg    string
}

var osFamilyRules = []osFamilyRule{
	// macOS first: "darwin" collides with nothing, but brew is not a Linux
	// package family and should not fall through to one.
	{[]string{"darwin", "macos", "mac os"}, "macos", "brew"},

	// Debian and its derivatives. Pop!_OS and Mint precede Ubuntu, and Ubuntu
	// precedes Debian, because each reports its own name and is a derivative
	// of the next.
	{[]string{"pop!_os", "pop_os", "pop-os", "pop os"}, "pop-os", "deb"},
	{[]string{"linux mint", "linuxmint"}, "mint", "deb"},
	{[]string{"raspbian", "raspberry pi os"}, "raspbian", "deb"},
	{[]string{"kali"}, "kali", "deb"},
	{[]string{"devuan"}, "devuan", "deb"},
	{[]string{"ubuntu"}, "ubuntu", "deb"},
	{[]string{"debian"}, "debian", "deb"},

	// The RHEL family. The rebuilds name themselves, so they are tested before
	// "red hat" to keep each one its own os: group while they share pkg:rpm.
	{[]string{"rocky"}, "rocky", "rpm"},
	{[]string{"almalinux", "alma linux"}, "alma", "rpm"},
	{[]string{"centos"}, "centos", "rpm"},
	{[]string{"oracle linux"}, "oracle", "rpm"},
	{[]string{"amazon linux"}, "amazon", "rpm"},
	{[]string{"scientific linux"}, "scientific", "rpm"},
	{[]string{"red hat", "redhat", "rhel"}, "rhel", "rpm"},
	{[]string{"fedora"}, "fedora", "rpm"},

	// SUSE is rpm-based but reaches it through zypper. It is still pkg:rpm:
	// the question "which hosts take an rpm" has one answer, and the update
	// script picks the manager per host anyway.
	{[]string{"opensuse", "suse"}, "suse", "rpm"},

	// The rest, each its own package family.
	{[]string{"nixos"}, "nixos", "nix"},
	{[]string{"manjaro"}, "manjaro", "pacman"},
	{[]string{"endeavouros"}, "endeavouros", "pacman"},
	{[]string{"arch linux", "archlinux"}, "arch", "pacman"},
	{[]string{"gentoo"}, "gentoo", "portage"},
	{[]string{"alpine"}, "alpine", "apk"},
	{[]string{"void linux"}, "void", "xbps"},
}

// OSFamily returns the distribution slug for an OS string, or "" when the
// distribution is not one this table knows.
func OSFamily(os string) string {
	lower := strings.ToLower(os)
	for _, rule := range osFamilyRules {
		for _, needle := range rule.match {
			if strings.Contains(lower, needle) {
				return rule.family
			}
		}
	}
	return ""
}

// PackageFamily returns the package family slug for an OS string ("rpm",
// "deb", ...), or "" when it cannot be determined.
//
// It is inferred from the distribution rather than reported by the host. The
// client knows exactly which package manager it used, so reporting it would be
// more precise; inferring keeps this entirely server-side and works for every
// host already checking in, including ones running an older client.
func PackageFamily(os string) string {
	lower := strings.ToLower(os)
	for _, rule := range osFamilyRules {
		for _, needle := range rule.match {
			if strings.Contains(lower, needle) {
				return rule.pkg
			}
		}
	}
	return ""
}

// DeriveGroups computes every derived group that currently has a member.
//
// Groups with no members are not returned at all, so the dashboard's chip bar
// shows the fleet that exists rather than a list of everything it might
// contain.
func DeriveGroups(systems []System) []Group {
	members := map[string][]string{}
	add := func(name, hostname string) {
		members[name] = append(members[name], hostname)
	}

	for _, system := range systems {
		host := strings.TrimSpace(system.Hostname)
		if host == "" {
			continue
		}

		if family := OSFamily(system.OS); family != "" {
			add(DerivedPrefixOS+family, host)
		}
		if pkg := PackageFamily(system.OS); pkg != "" {
			add(DerivedPrefixPkg+pkg, host)
		}
		if arch := strings.TrimSpace(system.Architecture); arch != "" {
			add(DerivedPrefixArch+strings.ToLower(arch), host)
		}

		// State groups move under you as hosts check in, unlike the three
		// above. That is what makes them useful — "everything that needs a
		// reboot" is exactly the set you want to reboot — and a group action
		// resolves its members when it runs, not when the page was drawn.
		if system.RebootRequired {
			add(DerivedPrefixState+"needs-reboot", host)
		}
		if system.UpdatesAvailable {
			add(DerivedPrefixState+"has-updates", host)
		}
	}

	groups := make([]Group, 0, len(members))
	for name, hosts := range members {
		sort.Strings(hosts)
		groups = append(groups, Group{Name: name, Members: hosts, Derived: true})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	return groups
}

// FindDerivedGroup returns the derived group of that name, if it currently has
// any members. The lookup is case-insensitive, like a stored group's.
func FindDerivedGroup(systems []System, name string) (Group, bool) {
	name = strings.TrimSpace(name)
	for _, group := range DeriveGroups(systems) {
		if SameGroupName(group.Name, name) {
			return group, true
		}
	}
	return Group{}, false
}
