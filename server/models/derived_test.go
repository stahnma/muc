package models

import (
	"slices"
	"testing"
)

// TestOSFamilyAndPackageFamily is the matching table's guard. The OS string is
// whatever the distribution put in its own PRETTY_NAME, so these are the real
// shapes rather than invented ones.
func TestOSFamilyAndPackageFamily(t *testing.T) {
	for _, tc := range []struct {
		os     string
		family string
		pkg    string
	}{
		{"Fedora Linux 42 (Workstation Edition)", "fedora", "rpm"},
		{"Rocky Linux 10.2 (Red Quartz)", "rocky", "rpm"},
		{"AlmaLinux 9.4 (Seafoam Ocelot)", "alma", "rpm"},
		{"CentOS Stream 10", "centos", "rpm"},
		{"Red Hat Enterprise Linux 9.4 (Plow)", "rhel", "rpm"},
		{"openSUSE Tumbleweed", "suse", "rpm"},
		{"Debian GNU/Linux 12 (bookworm)", "debian", "deb"},
		{"Ubuntu 24.04.1 LTS", "ubuntu", "deb"},
		{"Linux Mint 22", "mint", "deb"},
		{"Raspbian GNU/Linux 11 (bullseye)", "raspbian", "deb"},
		{"NixOS 24.11 (Vicuna)", "nixos", "nix"},
		{"Arch Linux", "arch", "pacman"},
		{"Manjaro Linux", "manjaro", "pacman"},
		{"Gentoo Linux", "gentoo", "portage"},
		{"Alpine Linux v3.20", "alpine", "apk"},
		{"macOS 15.1", "macos", "brew"},

		// Not in the table: joins no os: or pkg: group rather than being
		// guessed at. Guessing is how a host lands in "all rpm" and gets
		// handed a dnf command it cannot run.
		{"Slackware 15.0", "", ""},
		{"", "", ""},
	} {
		t.Run(tc.os, func(t *testing.T) {
			if got := OSFamily(tc.os); got != tc.family {
				t.Errorf("OSFamily(%q) = %q, want %q", tc.os, got, tc.family)
			}
			if got := PackageFamily(tc.os); got != tc.pkg {
				t.Errorf("PackageFamily(%q) = %q, want %q", tc.os, got, tc.pkg)
			}
		})
	}
}

// TestOSFamilyChecksDerivativesFirst pins the ordering that the table depends
// on. Pop!_OS and Mint are Ubuntu derivatives and Ubuntu is a Debian one, so a
// table tested in the wrong order collapses all three into "debian" and the
// os: groups stop telling them apart. The package family is shared either way,
// which is exactly what makes the bug quiet.
func TestOSFamilyChecksDerivativesFirst(t *testing.T) {
	for _, tc := range []struct{ os, want string }{
		{"Pop!_OS 22.04 LTS", "pop-os"},
		{"Linux Mint 22 (Wilma)", "mint"},
		{"Ubuntu 24.04.1 LTS", "ubuntu"},
		{"Rocky Linux 10.2", "rocky"},
		{"AlmaLinux 9.4", "alma"},
		{"Red Hat Enterprise Linux 9.4", "rhel"},
	} {
		if got := OSFamily(tc.os); got != tc.want {
			t.Errorf("OSFamily(%q) = %q, want %q", tc.os, got, tc.want)
		}
	}
}

func TestIsDerivedName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"os:fedora", true},
		{"pkg:rpm", true},
		{"arch:x86_64", true},
		{"state:needs-reboot", true},
		{"OS:Fedora", true},
		{" os:fedora ", true},
		{"prod", false},
		{"web servers", false},
		{"", false},
	} {
		if got := IsDerivedName(tc.name); got != tc.want {
			t.Errorf("IsDerivedName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestValidateGroupNameReservesColon: the derived namespace only works as a
// namespace if nothing can be hand-made inside it. A stored "os:fedora"
// sitting beside a computed one would make a group action ambiguous about
// which set of hosts it was acting on.
func TestValidateGroupNameReservesColon(t *testing.T) {
	if err := ValidateGroupName("os:fedora"); err == nil {
		t.Error("a name in the derived namespace was accepted")
	}
	if err := ValidateGroupName("prod"); err != nil {
		t.Errorf("an ordinary name was rejected: %v", err)
	}
}

func fleet() []System {
	return []System{
		{Hostname: "web01", OS: "Fedora Linux 42", Architecture: "x86_64", RebootRequired: true, UpdatesAvailable: true},
		{Hostname: "web02", OS: "Fedora Linux 42", Architecture: "x86_64"},
		{Hostname: "db01", OS: "Rocky Linux 10.2", Architecture: "aarch64", UpdatesAvailable: true},
		{Hostname: "build01", OS: "Ubuntu 24.04.1 LTS", Architecture: "x86_64", RebootRequired: true},
	}
}

func groupNames(groups []Group) []string {
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	return names
}

func memberList(t *testing.T, groups []Group, name string) []string {
	t.Helper()
	for _, g := range groups {
		if g.Name == name {
			return g.Members
		}
	}
	t.Fatalf("no derived group %q in %v", name, groupNames(groups))
	return nil
}

func TestDeriveGroups(t *testing.T) {
	groups := DeriveGroups(fleet())

	want := []string{
		"arch:aarch64", "arch:x86_64",
		"os:fedora", "os:rocky", "os:ubuntu",
		"pkg:deb", "pkg:rpm",
		"state:has-updates", "state:needs-reboot",
	}
	if got := groupNames(groups); !slices.Equal(got, want) {
		t.Errorf("groups = %v\nwant     %v", got, want)
	}

	// The whole point of pkg: — Fedora and Rocky are different distributions
	// and the same package family.
	if got := memberList(t, groups, "pkg:rpm"); !slices.Equal(got, []string{"db01", "web01", "web02"}) {
		t.Errorf("pkg:rpm = %v, want [db01 web01 web02]", got)
	}
	if got := memberList(t, groups, "pkg:deb"); !slices.Equal(got, []string{"build01"}) {
		t.Errorf("pkg:deb = %v, want [build01]", got)
	}
	if got := memberList(t, groups, "os:fedora"); !slices.Equal(got, []string{"web01", "web02"}) {
		t.Errorf("os:fedora = %v, want [web01 web02]", got)
	}
	if got := memberList(t, groups, "state:needs-reboot"); !slices.Equal(got, []string{"build01", "web01"}) {
		t.Errorf("state:needs-reboot = %v, want [build01 web01]", got)
	}

	for _, g := range groups {
		if !g.Derived {
			t.Errorf("%s is not marked derived", g.Name)
		}
	}
}

// TestDeriveGroupsOmitsEmptyOnes: the chip bar should show the fleet that
// exists, not a catalogue of every distribution this server has heard of.
func TestDeriveGroupsOmitsEmptyOnes(t *testing.T) {
	groups := DeriveGroups([]System{
		{Hostname: "web01", OS: "Fedora Linux 42", Architecture: "x86_64"},
	})

	for _, name := range []string{"pkg:deb", "os:debian", "state:needs-reboot", "state:has-updates"} {
		if slices.Contains(groupNames(groups), name) {
			t.Errorf("%s was derived with no members", name)
		}
	}
}

// TestDeriveGroupsSkipsUnknownDistros: an unrecognised OS still gets its arch
// group, because the architecture is reported rather than inferred. It just
// joins no os: or pkg: group.
func TestDeriveGroupsSkipsUnknownDistros(t *testing.T) {
	groups := DeriveGroups([]System{
		{Hostname: "odd01", OS: "Slackware 15.0", Architecture: "x86_64"},
	})

	if got := groupNames(groups); !slices.Equal(got, []string{"arch:x86_64"}) {
		t.Errorf("groups = %v, want [arch:x86_64]", got)
	}
}

func TestDeriveGroupsOnEmptyFleet(t *testing.T) {
	if groups := DeriveGroups(nil); len(groups) != 0 {
		t.Errorf("got %d groups from an empty fleet, want 0", len(groups))
	}
}

func TestFindDerivedGroup(t *testing.T) {
	systems := fleet()

	group, ok := FindDerivedGroup(systems, "PKG:RPM")
	if !ok {
		t.Fatal("a derived group was not found through a differently-cased name")
	}
	if group.Name != "pkg:rpm" || !group.Derived {
		t.Errorf("got %+v, want the derived pkg:rpm", group)
	}

	if _, ok := FindDerivedGroup(systems, "pkg:apk"); ok {
		t.Error("a derived group with no members was reported as existing")
	}
}
