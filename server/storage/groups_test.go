package storage

import (
	"path/filepath"
	"server/models"
	"slices"
	"testing"
)

// newTestStore opens a database in a directory the test owns, so nothing here
// can reach the real systems.db.
func newTestStore(t *testing.T) *BboltStorage {
	t.Helper()

	store, err := NewBboltStorage(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func mustSaveGroup(t *testing.T, store *BboltStorage, name string, members ...string) {
	t.Helper()
	if err := store.SaveGroup(models.Group{Name: name, Members: members}); err != nil {
		t.Fatalf("saving group %q: %v", name, err)
	}
}

func mustGetGroup(t *testing.T, store *BboltStorage, name string) models.Group {
	t.Helper()
	group, err := store.GetGroup(name)
	if err != nil {
		t.Fatalf("getting group %q: %v", name, err)
	}
	return group
}

// TestGroupRoundTrip is the baseline: what goes in comes back out, with the
// name spelled as it was typed.
func TestGroupRoundTrip(t *testing.T) {
	store := newTestStore(t)

	mustSaveGroup(t, store, "Production", "web01", "db01")

	group := mustGetGroup(t, store, "Production")
	if group.Name != "Production" {
		t.Errorf("Name = %q, want %q", group.Name, "Production")
	}
	if !slices.Equal(group.Members, []string{"db01", "web01"}) {
		t.Errorf("Members = %v, want [db01 web01]", group.Members)
	}
}

// TestGetGroupIgnoresCase pins the lookup half of the case rule. A URL that
// names "prod" must find the group someone created as "Prod", or the dashboard
// and the API would disagree about which groups exist.
func TestGetGroupIgnoresCase(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "Prod", "web01")

	if _, err := store.GetGroup("PROD"); err != nil {
		t.Errorf("GetGroup(%q) = %v, want the group created as %q", "PROD", err, "Prod")
	}
}

// TestSaveGroupDoesNotForkOnCase is the other half, and the one that matters:
// saving "prod" over "Prod" must replace it rather than create a second group.
// Two groups whose names differ only in case both look right in the chip bar,
// and an action on either one silently misses the hosts in the other.
func TestSaveGroupDoesNotForkOnCase(t *testing.T) {
	store := newTestStore(t)

	mustSaveGroup(t, store, "Prod", "web01")
	mustSaveGroup(t, store, "prod", "web01", "db01")

	groups, err := store.GetAllGroups()
	if err != nil {
		t.Fatalf("GetAllGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(groups), groups)
	}
	if groups[0].Name != "prod" {
		t.Errorf("Name = %q, want the spelling from the later save, %q", groups[0].Name, "prod")
	}
}

// TestSaveGroupNormalizesMembers checks a member list is stored sorted and
// de-duplicated, so the dashboard never has to sort and a double-add is not
// visible as two rows.
func TestSaveGroupNormalizesMembers(t *testing.T) {
	store := newTestStore(t)

	mustSaveGroup(t, store, "prod", "web02", "db01", "web02", "  ", "web01")

	group := mustGetGroup(t, store, "prod")
	if !slices.Equal(group.Members, []string{"db01", "web01", "web02"}) {
		t.Errorf("Members = %v, want [db01 web01 web02]", group.Members)
	}
}

// TestGetAllGroupsOnEmptyDatabase pins that a database where no group has ever
// been created reads as no groups rather than as an error. The bucket does not
// exist until the first write, and the dashboard asks for this on every load.
func TestGetAllGroupsOnEmptyDatabase(t *testing.T) {
	store := newTestStore(t)

	groups, err := store.GetAllGroups()
	if err != nil {
		t.Fatalf("GetAllGroups on an empty database: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("got %d groups, want 0", len(groups))
	}
}

func TestGetGroupNotFound(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "prod")

	if _, err := store.GetGroup("staging"); err == nil {
		t.Error("GetGroup on a group that does not exist returned no error")
	}
}

func TestDeleteGroup(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "prod", "web01")

	if err := store.DeleteGroup("PROD"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := store.GetGroup("prod"); err == nil {
		t.Error("the group is still there after DeleteGroup")
	}
	if err := store.DeleteGroup("prod"); err == nil {
		t.Error("deleting a group twice returned no error the second time")
	}
}

// TestDeleteGroupLeavesSystemsAlone: a group is a label. Dropping the label is
// not dropping the machines.
func TestDeleteGroupLeavesSystemsAlone(t *testing.T) {
	store := newTestStore(t)
	if err := store.SaveSystem("web01", models.System{Hostname: "web01"}); err != nil {
		t.Fatalf("SaveSystem: %v", err)
	}
	mustSaveGroup(t, store, "prod", "web01")

	if err := store.DeleteGroup("prod"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := store.GetSystem("web01"); err != nil {
		t.Errorf("deleting a group took its member with it: %v", err)
	}
}

// TestRenameGroupKeepsMembers is the point of having a rename at all: a delete
// and a create would drop everyone in the group.
func TestRenameGroupKeepsMembers(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "prod", "web01", "db01")

	if err := store.RenameGroup("prod", "production"); err != nil {
		t.Fatalf("RenameGroup: %v", err)
	}

	if _, err := store.GetGroup("prod"); err == nil {
		t.Error("the old name still resolves after a rename")
	}
	group := mustGetGroup(t, store, "production")
	if !slices.Equal(group.Members, []string{"db01", "web01"}) {
		t.Errorf("Members = %v, want [db01 web01]", group.Members)
	}
}

// TestRenameGroupToDifferentCase is a relabel in place, not a collision with
// itself — the key does not change, so the group must survive.
func TestRenameGroupToDifferentCase(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "prod", "web01")

	if err := store.RenameGroup("prod", "Prod"); err != nil {
		t.Fatalf("RenameGroup to a different case: %v", err)
	}

	group := mustGetGroup(t, store, "prod")
	if group.Name != "Prod" {
		t.Errorf("Name = %q, want %q", group.Name, "Prod")
	}
	if !slices.Equal(group.Members, []string{"web01"}) {
		t.Errorf("Members = %v, want [web01]", group.Members)
	}
}

func TestRenameGroupRefusesCollision(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "prod", "web01")
	mustSaveGroup(t, store, "staging", "web02")

	if err := store.RenameGroup("prod", "STAGING"); err == nil {
		t.Fatal("renaming onto an existing group returned no error")
	}

	// And neither group was damaged by the attempt.
	if group := mustGetGroup(t, store, "prod"); !slices.Equal(group.Members, []string{"web01"}) {
		t.Errorf("prod members = %v, want [web01]", group.Members)
	}
	if group := mustGetGroup(t, store, "staging"); !slices.Equal(group.Members, []string{"web02"}) {
		t.Errorf("staging members = %v, want [web02]", group.Members)
	}
}

func TestRenameGroupNotFound(t *testing.T) {
	store := newTestStore(t)

	if err := store.RenameGroup("nope", "also-nope"); err == nil {
		t.Error("renaming a group that does not exist returned no error")
	}
}

// TestSetHostGroupsJoinsAndLeaves is the shape of the dashboard's edit: the
// caller sends the complete set of groups the host should be in, so a group
// left out of the list is a group the host leaves.
func TestSetHostGroupsJoinsAndLeaves(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "prod", "web01", "db01")
	mustSaveGroup(t, store, "web")
	mustSaveGroup(t, store, "staging")

	if err := store.SetHostGroups("web01", []string{"web", "staging"}); err != nil {
		t.Fatalf("SetHostGroups: %v", err)
	}

	if group := mustGetGroup(t, store, "prod"); slices.Contains(group.Members, "web01") {
		t.Errorf("web01 is still in prod, which was not in the list: %v", group.Members)
	}
	if group := mustGetGroup(t, store, "web"); !slices.Contains(group.Members, "web01") {
		t.Errorf("web01 did not join web: %v", group.Members)
	}
	if group := mustGetGroup(t, store, "staging"); !slices.Contains(group.Members, "web01") {
		t.Errorf("web01 did not join staging: %v", group.Members)
	}

	// The other member of prod was not collateral damage.
	if group := mustGetGroup(t, store, "prod"); !slices.Equal(group.Members, []string{"db01"}) {
		t.Errorf("prod members = %v, want [db01]", group.Members)
	}
}

// TestSetHostGroupsToNothing: an empty list is how a host leaves every group,
// and must not be mistaken for "change nothing".
func TestSetHostGroupsToNothing(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "prod", "web01")
	mustSaveGroup(t, store, "web", "web01")

	if err := store.SetHostGroups("web01", nil); err != nil {
		t.Fatalf("SetHostGroups with no groups: %v", err)
	}

	for _, name := range []string{"prod", "web"} {
		if group := mustGetGroup(t, store, name); len(group.Members) != 0 {
			t.Errorf("%s members = %v, want none", name, group.Members)
		}
	}
}

// TestSetHostGroupsRefusesUnknownGroup pins that a typo joins nothing rather
// than quietly founding a group of one. Creating a group is a separate,
// deliberate act.
func TestSetHostGroupsRefusesUnknownGroup(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "prod")
	mustSaveGroup(t, store, "web")

	if err := store.SetHostGroups("web01", []string{"web", "prodd"}); err == nil {
		t.Fatal("naming a group that does not exist returned no error")
	}

	// Nothing was written: the valid half of the list must not have landed.
	if group := mustGetGroup(t, store, "web"); slices.Contains(group.Members, "web01") {
		t.Errorf("web01 joined web even though the call failed: %v", group.Members)
	}
}

// TestSetHostGroupsIgnoresCase: the dashboard sends back whatever spelling it
// was given, so matching has to be as forgiving as GetGroup.
func TestSetHostGroupsIgnoresCase(t *testing.T) {
	store := newTestStore(t)
	mustSaveGroup(t, store, "Prod")

	if err := store.SetHostGroups("web01", []string{"PROD"}); err != nil {
		t.Fatalf("SetHostGroups: %v", err)
	}
	if group := mustGetGroup(t, store, "prod"); !slices.Contains(group.Members, "web01") {
		t.Errorf("web01 did not join Prod: %v", group.Members)
	}
}

// TestDeleteSystemRemovesItFromEveryGroup is the one that would otherwise rot
// quietly. A deleted host left behind in its groups is a member nothing can act
// on, padding the member count of every group action from then on.
func TestDeleteSystemRemovesItFromEveryGroup(t *testing.T) {
	store := newTestStore(t)
	for _, host := range []string{"web01", "web02"} {
		if err := store.SaveSystem(host, models.System{Hostname: host}); err != nil {
			t.Fatalf("SaveSystem %s: %v", host, err)
		}
	}
	mustSaveGroup(t, store, "prod", "web01", "web02")
	mustSaveGroup(t, store, "web", "web01")
	mustSaveGroup(t, store, "staging", "web02")

	if err := store.DeleteSystem("web01"); err != nil {
		t.Fatalf("DeleteSystem: %v", err)
	}

	if group := mustGetGroup(t, store, "prod"); !slices.Equal(group.Members, []string{"web02"}) {
		t.Errorf("prod members = %v, want [web02]", group.Members)
	}
	if group := mustGetGroup(t, store, "web"); len(group.Members) != 0 {
		t.Errorf("web members = %v, want none", group.Members)
	}
	// A group the host was never in is untouched.
	if group := mustGetGroup(t, store, "staging"); !slices.Equal(group.Members, []string{"web02"}) {
		t.Errorf("staging members = %v, want [web02]", group.Members)
	}
}

// TestDeleteSystemWithNoGroups pins that the purge is harmless on a database
// where no group has ever been created — the groups bucket does not exist yet,
// and deleting a host must not start failing because of it.
func TestDeleteSystemWithNoGroups(t *testing.T) {
	store := newTestStore(t)
	if err := store.SaveSystem("web01", models.System{Hostname: "web01"}); err != nil {
		t.Fatalf("SaveSystem: %v", err)
	}

	if err := store.DeleteSystem("web01"); err != nil {
		t.Errorf("DeleteSystem on a database with no groups: %v", err)
	}
}

// TestGroupMembersMayNameUnknownHosts: membership is not a foreign key. A group
// built before its hosts are provisioned, or one holding a host that was
// deleted and will be re-added, is a legitimate state.
func TestGroupMembersMayNameUnknownHosts(t *testing.T) {
	store := newTestStore(t)

	mustSaveGroup(t, store, "prod", "not-yet-built")

	if group := mustGetGroup(t, store, "prod"); !slices.Equal(group.Members, []string{"not-yet-built"}) {
		t.Errorf("Members = %v, want [not-yet-built]", group.Members)
	}
}

func TestValidateGroupName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"ordinary", "prod", false},
		{"spaces inside", "web servers", false},
		{"empty", "", true},
		{"path separator", "prod/web", true},
		{"control character", "prod\nweb", true},
		{"at the limit", string(make([]byte, 0, 64)) + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"over the limit", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := models.ValidateGroupName(tc.input)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateGroupName(%q) = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
		})
	}
}
