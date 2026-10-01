package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"server/models"
	"slices"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

// storeWithGroups builds a fakeStorage holding the given groups, keyed and
// sorted the way the bbolt layer keys and sorts them — a fake that is tidier
// than the real store would hide an ordering bug rather than catch one.
func storeWithGroups(systems []models.System, groups ...models.Group) *fakeStorage {
	store := &fakeStorage{systems: systems, groups: map[string]models.Group{}}
	for _, g := range groups {
		slices.Sort(g.Members)
		store.groups[strings.ToLower(g.Name)] = g
	}
	return store
}

func doJSON(handler http.HandlerFunc, method, path, body string, vars map[string]string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("{}")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if vars != nil {
		req = mux.SetURLVars(req, vars)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func decodeGroup(t *testing.T, rec *httptest.ResponseRecorder) models.Group {
	t.Helper()
	var group models.Group
	if err := json.NewDecoder(rec.Body).Decode(&group); err != nil {
		t.Fatalf("decoding group: %v", err)
	}
	return group
}

func decodeAction(t *testing.T, rec *httptest.ResponseRecorder) GroupActionResponse {
	t.Helper()
	var resp GroupActionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding group action response: %v", err)
	}
	return resp
}

// TestListGroupsOnEmptyServerReturnsAnArray: the dashboard iterates the
// response without checking it first, and a JSON null would throw. An empty
// fleet is the state every new install starts in.
func TestListGroupsOnEmptyServerReturnsAnArray(t *testing.T) {
	rec := doJSON(ListGroupsHandler(&fakeStorage{}), http.MethodGet, "/api/groups", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %s, want []", body)
	}
}

func TestCreateGroup(t *testing.T) {
	store := &fakeStorage{}

	rec := doJSON(CreateGroupHandler(store), http.MethodPost, "/api/groups", `{"name":"prod"}`, nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if group := decodeGroup(t, rec); group.Name != "prod" {
		t.Errorf("Name = %q, want %q", group.Name, "prod")
	}
}

// TestCreateGroupRefusesCaseDuplicate is the one name mistake worth designing
// against: "prod" and "Prod" both look right in the chip bar, and an action on
// either silently misses the hosts in the other.
func TestCreateGroupRefusesCaseDuplicate(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "Prod", Members: []string{"web01"}})

	rec := doJSON(CreateGroupHandler(store), http.MethodPost, "/api/groups", `{"name":"prod"}`, nil)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	// The message names the group as it is actually spelled, which is the
	// thing the operator needs to go and look at.
	if !strings.Contains(rec.Body.String(), "Prod") {
		t.Errorf("the refusal does not name the existing group: %s", rec.Body.String())
	}
}

func TestCreateGroupRejectsBadNames(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", `{"name":""}`},
		{"whitespace only", `{"name":"   "}`},
		{"path separator", `{"name":"prod/web"}`},
		{"too long", `{"name":"` + strings.Repeat("a", 65) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(CreateGroupHandler(&fakeStorage{}), http.MethodPost, "/api/groups", tc.body, nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestGetGroupNotFound(t *testing.T) {
	rec := doJSON(GetGroupHandler(&fakeStorage{}), http.MethodGet, "/api/groups/nope", "",
		map[string]string{"group": "nope"})

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestUpdateGroupOmittingMembersLeavesThemAlone is why the request struct uses
// pointers: a rename must not be a way to accidentally empty a group.
func TestUpdateGroupOmittingMembersLeavesThemAlone(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod", Members: []string{"web01", "db01"}})

	rec := doJSON(UpdateGroupHandler(store), http.MethodPut, "/api/groups/prod",
		`{"name":"production"}`, map[string]string{"group": "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	group := decodeGroup(t, rec)
	if group.Name != "production" {
		t.Errorf("Name = %q, want %q", group.Name, "production")
	}
	if !slices.Equal(group.Members, []string{"db01", "web01"}) {
		t.Errorf("a rename lost the members: %v", group.Members)
	}
}

// TestUpdateGroupWithEmptyMembersEmptiesIt is the other half of the pointer:
// sending an explicit empty list must mean what it says.
func TestUpdateGroupWithEmptyMembersEmptiesIt(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod", Members: []string{"web01"}})

	rec := doJSON(UpdateGroupHandler(store), http.MethodPut, "/api/groups/prod",
		`{"members":[]}`, map[string]string{"group": "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if group := decodeGroup(t, rec); len(group.Members) != 0 {
		t.Errorf("Members = %v, want none", group.Members)
	}
}

func TestUpdateGroupRenameCollision(t *testing.T) {
	store := storeWithGroups(nil,
		models.Group{Name: "prod", Members: []string{"web01"}},
		models.Group{Name: "staging", Members: []string{"web02"}},
	)

	rec := doJSON(UpdateGroupHandler(store), http.MethodPut, "/api/groups/prod",
		`{"name":"STAGING"}`, map[string]string{"group": "prod"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteGroup(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod"})

	rec := doJSON(DeleteGroupHandler(store), http.MethodDelete, "/api/groups/prod", "",
		map[string]string{"group": "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(store.groups) != 0 {
		t.Errorf("the group is still in storage: %v", store.groups)
	}
}

func TestDeleteGroupNotFound(t *testing.T) {
	rec := doJSON(DeleteGroupHandler(&fakeStorage{groups: map[string]models.Group{}}),
		http.MethodDelete, "/api/groups/nope", "", map[string]string{"group": "nope"})

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestRemoveGroupMemberEvictsAHostTheServerDoesNotKnow is the whole point of
// the route: a decommissioned machine has no row to expand, so the per-host
// editor cannot reach it.
func TestRemoveGroupMemberEvictsAHostTheServerDoesNotKnow(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod", Members: []string{"web01", "retired01"}})

	rec := doJSON(RemoveGroupMemberHandler(store), http.MethodDelete,
		"/api/groups/prod/members/retired01", "",
		map[string]string{"group": "prod", "hostname": "retired01"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if group := decodeGroup(t, rec); !slices.Equal(group.Members, []string{"web01"}) {
		t.Errorf("Members = %v, want [web01]", group.Members)
	}
}

func TestRemoveGroupMemberNotAMember(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod", Members: []string{"web01"}})

	rec := doJSON(RemoveGroupMemberHandler(store), http.MethodDelete,
		"/api/groups/prod/members/db01", "",
		map[string]string{"group": "prod", "hostname": "db01"})

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSetSystemGroups(t *testing.T) {
	store := storeWithGroups(nil,
		models.Group{Name: "prod", Members: []string{"web01"}},
		models.Group{Name: "web"},
	)

	rec := doJSON(SetSystemGroupsHandler(store), http.MethodPut, "/api/systems/web01/groups",
		`{"groups":["web"]}`, map[string]string{"hostname": "web01"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Hostname string   `json:"hostname"`
		Groups   []string `json:"groups"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !slices.Equal(got.Groups, []string{"web"}) {
		t.Errorf("Groups = %v, want [web] — the host should have left prod", got.Groups)
	}
}

// TestSetSystemGroupsRefusesUnknownGroup: a typo must join nothing rather than
// found a group of one, which is far harder to spot afterwards.
func TestSetSystemGroupsRefusesUnknownGroup(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod"})

	rec := doJSON(SetSystemGroupsHandler(store), http.MethodPut, "/api/systems/web01/groups",
		`{"groups":["prodd"]}`, map[string]string{"hostname": "web01"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if group := store.groups["prod"]; slices.Contains(group.Members, "web01") {
		t.Error("the valid half of a refused list was still applied")
	}
}

// TestSetSystemGroupsWorksForAHostThatHasNeverCheckedIn: pre-staging a machine
// into its groups before it is built is a reasonable thing to want, and
// membership already outlives the system record.
func TestSetSystemGroupsWorksForAHostThatHasNeverCheckedIn(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod"})

	rec := doJSON(SetSystemGroupsHandler(store), http.MethodPut, "/api/systems/not-yet-built/groups",
		`{"groups":["prod"]}`, map[string]string{"hostname": "not-yet-built"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if group := store.groups["prod"]; !slices.Contains(group.Members, "not-yet-built") {
		t.Errorf("the host did not join: %v", group.Members)
	}
}

// TestGroupActionDisabled pins the server-side half of each opt-in, and the
// deliberate difference between them: update and reboot are policy decisions
// and answer 403, while an unavailable check-in is an infrastructure fact and
// answers 503.
func TestGroupActionDisabled(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod", Members: []string{"web01"}})
	vars := map[string]string{"group": "prod"}

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{"update", GroupUpdateHandler(store, nil), http.StatusForbidden},
		{"reboot", GroupRebootHandler(store, nil), http.StatusForbidden},
		{"checkin", GroupCheckInHandler(store, nil), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(tc.handler, http.MethodPost, "/api/groups/prod/"+tc.name, "", vars)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestGroupActionUnknownGroup(t *testing.T) {
	store := &fakeStorage{groups: map[string]models.Group{}}
	updater := &fakeUpdater{ack: models.UpdateAck{Accepted: true}}

	rec := doJSON(GroupUpdateHandler(store, updater), http.MethodPost, "/api/groups/nope/update", "",
		map[string]string{"group": "nope"})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if updater.callCount() != 0 {
		t.Error("an unknown group still dispatched commands")
	}
}

// TestGroupActionOnEmptyGroup: an empty group is a legitimate state, not an
// error. Keeping the client on one code path is worth more than the
// distinction, so it answers 200 with nothing requested.
func TestGroupActionOnEmptyGroup(t *testing.T) {
	store := storeWithGroups(nil, models.Group{Name: "prod"})
	updater := &fakeUpdater{ack: models.UpdateAck{Accepted: true}}

	rec := doJSON(GroupUpdateHandler(store, updater), http.MethodPost, "/api/groups/prod/update", "",
		map[string]string{"group": "prod"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeAction(t, rec)
	if resp.Requested != 0 {
		t.Errorf("Requested = %d, want 0", resp.Requested)
	}
	if resp.Results == nil {
		t.Error("Results is null; the dashboard iterates it without checking")
	}
}

// TestGroupUpdateMixedOutcomes is the case the whole feature turns on: one
// machine declining must not stop the rest being patched, and the operator has
// to be able to see which was which.
func TestGroupUpdateMixedOutcomes(t *testing.T) {
	optedOut := optedInSystem("db01")
	optedOut.RemoteUpdatesEnabled = false

	store := storeWithGroups(
		[]models.System{optedInSystem("web01"), optedOut, optedInSystem("web02")},
		models.Group{Name: "prod", Members: []string{"db01", "ghost01", "web01", "web02"}},
	)
	updater := &fakeUpdater{perHost: map[string]fakeAnswer{
		"web01": {accepted: true, id: "run-1"},
		"web02": {err: models.ErrHostNotListening},
	}}

	rec := doJSON(GroupUpdateHandler(store, updater), http.MethodPost, "/api/groups/prod/update", "",
		map[string]string{"group": "prod"})

	// 200, not 207 and not an error: the fan-out ran and the body says what
	// happened to each host.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	resp := decodeAction(t, rec)
	if resp.Requested != 4 || resp.Accepted != 1 || resp.Skipped != 2 || resp.Failed != 1 {
		t.Errorf("counts = requested %d / accepted %d / skipped %d / failed %d; want 4/1/2/1",
			resp.Requested, resp.Accepted, resp.Skipped, resp.Failed)
	}
	if resp.Group != "prod" || resp.Action != "update" {
		t.Errorf("Group/Action = %q/%q, want prod/update", resp.Group, resp.Action)
	}

	// One result per member, in the group's order, so the list reads the same
	// twice running.
	wantHosts := []string{"db01", "ghost01", "web01", "web02"}
	for i, want := range wantHosts {
		if resp.Results[i].Hostname != want {
			t.Errorf("Results[%d].Hostname = %q, want %q", i, resp.Results[i].Hostname, want)
		}
	}
}

// TestGroupRebootSkipsHostsWithNothingPending is what makes the button safe to
// press after a group update: it reboots what needs it and leaves the rest.
func TestGroupRebootSkipsHostsWithNothingPending(t *testing.T) {
	noPending := optedInSystem("web02")
	noPending.RebootRequired = false

	store := storeWithGroups(
		[]models.System{optedInSystem("web01"), noPending},
		models.Group{Name: "prod", Members: []string{"web01", "web02"}},
	)
	rebooter := &fakeRebooter{ack: models.RebootAck{Accepted: true, ID: "r1"}}

	rec := doJSON(GroupRebootHandler(store, rebooter), http.MethodPost, "/api/groups/prod/reboot", "",
		map[string]string{"group": "prod"})

	resp := decodeAction(t, rec)
	if resp.Accepted != 1 || resp.Skipped != 1 {
		t.Errorf("accepted %d / skipped %d, want 1/1", resp.Accepted, resp.Skipped)
	}
	if resp.Results[1].Code != CodeNoRebootPending {
		t.Errorf("Results[1].Code = %q, want %q", resp.Results[1].Code, CodeNoRebootPending)
	}
	if rebooter.callCount() != 1 {
		t.Errorf("%d hosts were sent a reboot, want 1", rebooter.callCount())
	}
}

// TestGroupActionIsCaseInsensitive: a chip the dashboard drew from the stored
// spelling has to work whatever case the URL carries.
func TestGroupActionIsCaseInsensitive(t *testing.T) {
	store := storeWithGroups(
		[]models.System{optedInSystem("web01")},
		models.Group{Name: "Prod", Members: []string{"web01"}},
	)
	checkins := &fakeCheckIner{ack: models.CheckInAck{Accepted: true, ID: "c1"}}

	rec := doJSON(GroupCheckInHandler(store, checkins), http.MethodPost, "/api/groups/PROD/checkin", "",
		map[string]string{"group": "PROD"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if resp := decodeAction(t, rec); resp.Group != "Prod" {
		t.Errorf("Group = %q, want the stored spelling %q", resp.Group, "Prod")
	}
}

// --- derived groups -------------------------------------------------------

// fedoraFleet is a set of hosts whose facts produce derived groups: two Fedora
// boxes and one Ubuntu one, with one of each opted into remote updates.
func fedoraFleet() []models.System {
	web01 := optedInSystem("web01")
	web01.OS = "Fedora Linux 42"
	web01.Architecture = "x86_64"

	web02 := optedInSystem("web02")
	web02.OS = "Fedora Linux 42"
	web02.Architecture = "x86_64"
	web02.RemoteUpdatesEnabled = false

	build01 := optedInSystem("build01")
	build01.OS = "Ubuntu 24.04.1 LTS"
	build01.Architecture = "x86_64"

	return []models.System{web01, web02, build01}
}

// TestListGroupsIncludesDerived: the dashboard reads one route and gets both
// kinds, so a chip bar needs no second request to be complete.
func TestListGroupsIncludesDerived(t *testing.T) {
	store := storeWithGroups(fedoraFleet(), models.Group{Name: "prod", Members: []string{"web01"}})

	rec := doJSON(ListGroupsHandler(store), http.MethodGet, "/api/groups", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var groups []models.Group
	if err := json.NewDecoder(rec.Body).Decode(&groups); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	byName := map[string]models.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}

	if g, ok := byName["prod"]; !ok {
		t.Error("the stored group is missing")
	} else if g.Derived {
		t.Error("a stored group is marked derived")
	}

	for _, name := range []string{"os:fedora", "os:ubuntu", "pkg:rpm", "pkg:deb", "arch:x86_64"} {
		g, ok := byName[name]
		if !ok {
			t.Errorf("derived group %q is missing", name)
			continue
		}
		if !g.Derived {
			t.Errorf("%q is not marked derived, so the dashboard cannot style it apart", name)
		}
	}

	if !slices.Equal(byName["pkg:rpm"].Members, []string{"web01", "web02"}) {
		t.Errorf("pkg:rpm = %v, want [web01 web02]", byName["pkg:rpm"].Members)
	}
}

// TestGroupActionOnDerivedGroup is the point of the whole feature: "update
// every Fedora box" without anyone maintaining a list of which ones they are.
func TestGroupActionOnDerivedGroup(t *testing.T) {
	store := storeWithGroups(fedoraFleet())
	updater := &fakeUpdater{perHost: map[string]fakeAnswer{
		"web01": {accepted: true, id: "run-1"},
	}}

	rec := doJSON(GroupUpdateHandler(store, updater), http.MethodPost, "/api/groups/os:fedora/update", "",
		map[string]string{"group": "os:fedora"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	resp := decodeAction(t, rec)
	if resp.Group != "os:fedora" {
		t.Errorf("Group = %q, want os:fedora", resp.Group)
	}
	// Both Fedora hosts were considered; the Ubuntu one was never in the group.
	if resp.Requested != 2 {
		t.Errorf("Requested = %d, want 2 (the two Fedora hosts)", resp.Requested)
	}
	if resp.Accepted != 1 || resp.Skipped != 1 {
		t.Errorf("accepted %d / skipped %d, want 1/1 (web02 has not opted in)", resp.Accepted, resp.Skipped)
	}
	if slices.Contains(updater.hostsSeen(), "build01") {
		t.Error("the Ubuntu host was asked to update as part of os:fedora")
	}
}

// TestGroupActionOnPackageFamily is the "all rpm" case, which is the one that
// spans distributions.
func TestGroupActionOnPackageFamily(t *testing.T) {
	fleet := fedoraFleet()
	rocky := optedInSystem("db01")
	rocky.OS = "Rocky Linux 10.2"
	rocky.Architecture = "aarch64"
	fleet = append(fleet, rocky)

	store := storeWithGroups(fleet)
	checkins := &fakeCheckIner{ack: models.CheckInAck{Accepted: true, ID: "c1"}}

	rec := doJSON(GroupCheckInHandler(store, checkins), http.MethodPost, "/api/groups/pkg:rpm/checkin", "",
		map[string]string{"group": "pkg:rpm"})

	resp := decodeAction(t, rec)
	if resp.Requested != 3 {
		t.Errorf("Requested = %d, want 3 (two Fedora and one Rocky)", resp.Requested)
	}
	hosts := []string{}
	for _, res := range resp.Results {
		hosts = append(hosts, res.Hostname)
	}
	if !slices.Equal(hosts, []string{"db01", "web01", "web02"}) {
		t.Errorf("members = %v, want [db01 web01 web02]", hosts)
	}
}

// TestDerivedGroupResolvesWhenTheActionRuns: a state group has to mean what is
// true now, not what was true when the page was drawn. This is what makes
// "reboot everything that needs a reboot" correct rather than approximate.
func TestDerivedGroupResolvesAtDispatchTime(t *testing.T) {
	needsReboot := optedInSystem("web01")
	needsReboot.OS = "Fedora Linux 42"
	noReboot := optedInSystem("web02")
	noReboot.OS = "Fedora Linux 42"
	noReboot.RebootRequired = false

	store := storeWithGroups([]models.System{needsReboot, noReboot})
	rebooter := &fakeRebooter{ack: models.RebootAck{Accepted: true, ID: "r1"}}

	rec := doJSON(GroupRebootHandler(store, rebooter), http.MethodPost, "/api/groups/state:needs-reboot/reboot", "",
		map[string]string{"group": "state:needs-reboot"})

	resp := decodeAction(t, rec)
	if resp.Requested != 1 || resp.Accepted != 1 {
		t.Errorf("requested %d / accepted %d, want 1/1", resp.Requested, resp.Accepted)
	}
	if resp.Results[0].Hostname != "web01" {
		t.Errorf("rebooted %q, want web01", resp.Results[0].Hostname)
	}
}

// TestDerivedGroupWithNoMembersIsNotFound: nothing is Debian, so there is no
// pkg:deb to act on. An empty group here would invite a button for a set that
// does not exist.
func TestDerivedGroupWithNoMembersIsNotFound(t *testing.T) {
	store := storeWithGroups(fedoraFleet())
	rebooter := &fakeRebooter{ack: models.RebootAck{Accepted: true}}

	rec := doJSON(GroupRebootHandler(store, rebooter), http.MethodPost, "/api/groups/os:debian/reboot", "",
		map[string]string{"group": "os:debian"})

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rebooter.callCount() != 0 {
		t.Error("a group with no members still dispatched commands")
	}
}

// TestDerivedGroupsCannotBeEdited. Every route that changes a group has to
// refuse, or membership would appear editable and then silently revert on the
// next read, which is worse than refusing.
func TestDerivedGroupsCannotBeEdited(t *testing.T) {
	store := storeWithGroups(fedoraFleet())
	vars := map[string]string{"group": "os:fedora"}

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    string
		vars    map[string]string
	}{
		{"rename", UpdateGroupHandler(store), http.MethodPut, `{"name":"fedora"}`, vars},
		{"set members", UpdateGroupHandler(store), http.MethodPut, `{"members":["web01"]}`, vars},
		{"delete", DeleteGroupHandler(store), http.MethodDelete, "", vars},
		{"remove member", RemoveGroupMemberHandler(store), http.MethodDelete, "",
			map[string]string{"group": "os:fedora", "hostname": "web01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(tc.handler, tc.method, "/api/groups/os:fedora", tc.body, tc.vars)
			if rec.Code != http.StatusConflict {
				t.Errorf("status = %d, want 409: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "derived") {
				t.Errorf("the refusal does not explain why: %s", rec.Body.String())
			}
		})
	}
}

// TestSetSystemGroupsRefusesDerived: a host joins os:fedora by being a Fedora
// box, not by being put there.
func TestSetSystemGroupsRefusesDerived(t *testing.T) {
	store := storeWithGroups(fedoraFleet(), models.Group{Name: "prod"})

	rec := doJSON(SetSystemGroupsHandler(store), http.MethodPut, "/api/systems/web01/groups",
		`{"groups":["prod","os:fedora"]}`, map[string]string{"hostname": "web01"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// Nothing was applied: the valid half of the list must not have landed.
	if group := store.groups["prod"]; slices.Contains(group.Members, "web01") {
		t.Error("the valid half of a refused list was still applied")
	}
}

// TestCreateGroupRefusesDerivedNamespace. The namespace only works if nothing
// can be hand-made inside it.
func TestCreateGroupRefusesDerivedNamespace(t *testing.T) {
	rec := doJSON(CreateGroupHandler(&fakeStorage{}), http.MethodPost, "/api/groups",
		`{"name":"os:fedora"}`, nil)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
