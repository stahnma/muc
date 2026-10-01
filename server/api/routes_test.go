package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"server/models"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/mux"
)

// fakeStorage is a minimal in-memory Storage for exercising the handlers.
type fakeStorage struct {
	systems []models.System
	err     error

	// groups is keyed by the group's name folded to lower case, mirroring what
	// the bbolt store does with its keys.
	groups map[string]models.Group
	// groupErr, when set, fails every group read and write, for the tests that
	// need the storage layer to break.
	groupErr error
}

func (f *fakeStorage) SaveSystem(hostname string, system models.System) error { return nil }

func (f *fakeStorage) GetSystem(hostname string) (models.System, error) {
	if f.err != nil {
		return models.System{}, f.err
	}
	for _, s := range f.systems {
		if s.Hostname == hostname {
			return s, nil
		}
	}
	return models.System{}, errNotFound{}
}

func (f *fakeStorage) GetAllSystems() ([]models.System, error) { return f.systems, f.err }
func (f *fakeStorage) DeleteSystem(hostname string) error      { return f.err }
func (f *fakeStorage) SubscribeToUpdates() <-chan models.System {
	return make(chan models.System)
}

func (f *fakeStorage) GetAllGroups() ([]models.Group, error) {
	if f.groupErr != nil {
		return nil, f.groupErr
	}
	groups := []models.Group{}
	for _, g := range f.groups {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Name) < strings.ToLower(groups[j].Name)
	})
	return groups, nil
}

func (f *fakeStorage) GetGroup(name string) (models.Group, error) {
	if f.groupErr != nil {
		return models.Group{}, f.groupErr
	}
	group, ok := f.groups[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return models.Group{}, errNotFound{}
	}
	return group, nil
}

func (f *fakeStorage) SaveGroup(group models.Group) error {
	if f.groupErr != nil {
		return f.groupErr
	}
	if f.groups == nil {
		f.groups = map[string]models.Group{}
	}
	sort.Strings(group.Members)
	f.groups[strings.ToLower(strings.TrimSpace(group.Name))] = group
	return nil
}

func (f *fakeStorage) DeleteGroup(name string) error {
	if f.groupErr != nil {
		return f.groupErr
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if _, ok := f.groups[key]; !ok {
		return errNotFound{}
	}
	delete(f.groups, key)
	return nil
}

func (f *fakeStorage) RenameGroup(oldName, newName string) error {
	if f.groupErr != nil {
		return f.groupErr
	}
	oldKey := strings.ToLower(strings.TrimSpace(oldName))
	newKey := strings.ToLower(strings.TrimSpace(newName))
	group, ok := f.groups[oldKey]
	if !ok {
		return errNotFound{}
	}
	if oldKey != newKey {
		if _, taken := f.groups[newKey]; taken {
			return errors.New("group already exists")
		}
		delete(f.groups, oldKey)
	}
	group.Name = newName
	f.groups[newKey] = group
	return nil
}

func (f *fakeStorage) SetHostGroups(hostname string, groups []string) error {
	if f.groupErr != nil {
		return f.groupErr
	}
	wanted := map[string]bool{}
	for _, name := range groups {
		key := strings.ToLower(strings.TrimSpace(name))
		if _, ok := f.groups[key]; !ok {
			return errNotFound{}
		}
		wanted[key] = true
	}
	for key, group := range f.groups {
		members := slices.DeleteFunc(slices.Clone(group.Members), func(m string) bool { return m == hostname })
		if wanted[key] {
			members = append(members, hostname)
		}
		sort.Strings(members)
		group.Members = members
		f.groups[key] = group
	}
	return nil
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }

// fullySetSystem returns a System with every field set to a distinctive non-zero
// value, so a field the handler forgets to copy shows up as a zero value.
func fullySetSystem() models.System {
	return models.System{
		Hostname:            "smallboi",
		Architecture:        "x86_64",
		Ip:                  "192.168.1.70",
		OS:                  "Rocky Linux 10.2 (Red Quartz)",
		OSVersion:           "10.2",
		UpdatesAvailable:    true,
		UpdateStatusUnknown: true,
		LastSeen:            "2026-07-31T23:40:41Z",
		ClientVersion:       "1.2.3",
		PendingUpdates:      []models.Update{{Name: "bash", Version: "5.2.26-4.el10", Source: "baseos"}},
		UpdatesCheckedAt:    "2026-07-31T23:40:40Z",
		UpdateCheckWarnings: []string{"repository skipped, update list may be incomplete: grafana"},
		CPUModel:            "AMD Ryzen 5 3550H",
		CPUCores:            8,
		MemoryTotalBytes:    14322552832,
		UptimeSeconds:       132141,
		RebootRequired:      true,
		Tailscale: &models.Tailscale{
			Connected:       true,
			Tailnet:         "mastahnke@gmail.com",
			MagicDNSSuffix:  "tail2ad946.ts.net",
			IP:              "100.126.136.109",
			State:           "Running",
			LastTailnet:     "mastahnke@gmail.com",
			LastConnectedAt: "2026-07-31T23:40:41Z",
		},
		RemoteUpdatesEnabled: true,
		LastUpdateRun: &models.UpdateRun{
			ID:          "9f2c1b0a4d5e6f70",
			Status:      "succeeded",
			RequestedBy: "192.168.1.20",
			Command:     "/usr/libexec/muc/upd",
			StartedAt:   "2026-07-31T23:38:02Z",
			FinishedAt:  "2026-07-31T23:40:38Z",
			ExitCode:    0,
			Error:       "",
			Output:      "upd: done\n",
		},
		RemoteRebootEnabled: true,
		LastReboot: &models.Reboot{
			ID:          "1a2b3c4d5e6f7081",
			Status:      "rebooted",
			RequestedBy: "192.168.1.20",
			Command:     "/usr/bin/systemctl reboot",
			RequestedAt: "2026-07-31T23:41:00Z",
			FinishedAt:  "2026-07-31T23:42:30Z",
		},
	}
}

// TestGetSystemsHandlerCopiesEveryField is the guard for the wire-through that
// adding a field to the dashboard requires. SystemSummary is populated by hand
// in GetSystemsHandler, so a new field can be declared on both structs and still
// silently serialise as its zero value if the assignment is forgotten — the
// dashboard then renders "never"/0/false rather than failing.
//
// Every field SystemSummary declares must arrive non-zero when the stored System
// had it set.
func TestGetSystemsHandlerCopiesEveryField(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}

	rec := httptest.NewRecorder()
	GetSystemsHandler(store)(rec, httptest.NewRequest(http.MethodGet, "/api/systems", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got []SystemSummary
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}

	v := reflect.ValueOf(got[0])
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		if v.Field(i).IsZero() {
			t.Errorf("SystemSummary.%s (json %q) came back as its zero value; "+
				"the source System had it set, so GetSystemsHandler is not copying it",
				field.Name, field.Tag.Get("json"))
		}
	}
}

// TestGetSystemsHandlerMatchesSourceValues checks the values are not merely
// non-zero but correct — a copy-paste that assigns the wrong source field would
// pass the zero-value check above.
func TestGetSystemsHandlerMatchesSourceValues(t *testing.T) {
	src := fullySetSystem()
	store := &fakeStorage{systems: []models.System{src}}

	rec := httptest.NewRecorder()
	GetSystemsHandler(store)(rec, httptest.NewRequest(http.MethodGet, "/api/systems", nil))

	var got []SystemSummary
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	summary := reflect.ValueOf(got[0])
	system := reflect.ValueOf(src)
	for i := 0; i < summary.NumField(); i++ {
		name := summary.Type().Field(i).Name
		srcField := system.FieldByName(name)
		if !srcField.IsValid() {
			t.Errorf("SystemSummary.%s has no counterpart on models.System", name)
			continue
		}
		// PendingUpdates differs only in element type ([]models.Update on both,
		// so DeepEqual is fine); everything else compares directly.
		if !reflect.DeepEqual(summary.Field(i).Interface(), srcField.Interface()) {
			t.Errorf("SystemSummary.%s = %#v, want %#v",
				name, summary.Field(i).Interface(), srcField.Interface())
		}
	}
}

// TestGetSystemsHandlerEmptyStoreReturnsArray guards against the store returning
// nil and the handler serialising `null`, which the dashboard's .map() would
// throw on.
func TestGetSystemsHandlerEmptyStoreReturnsArray(t *testing.T) {
	store := &fakeStorage{}

	rec := httptest.NewRecorder()
	GetSystemsHandler(store)(rec, httptest.NewRequest(http.MethodGet, "/api/systems", nil))

	if body := rec.Body.String(); body != "[]\n" {
		t.Errorf("body = %q, want %q", body, "[]\n")
	}
}

// TestGetSystemsHandlerStorageError checks a storage failure is a 500 rather
// than an empty list, which would read on the dashboard as "no systems".
func TestGetSystemsHandlerStorageError(t *testing.T) {
	store := &fakeStorage{err: errNotFound{}}

	rec := httptest.NewRecorder()
	GetSystemsHandler(store)(rec, httptest.NewRequest(http.MethodGet, "/api/systems", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestSystemSummaryCoversFreshnessFields pins the two fields that let the
// dashboard tell a freshly-checked host from one that keeps checking in while
// its update data goes stale. They are easy to drop in a refactor precisely
// because nothing else breaks when they are missing.
func TestSystemSummaryCoversFreshnessFields(t *testing.T) {
	summaryFields := map[string]bool{}
	st := reflect.TypeOf(SystemSummary{})
	for i := 0; i < st.NumField(); i++ {
		summaryFields[st.Field(i).Tag.Get("json")] = true
	}

	for _, want := range []string{
		"last_seen",
		"updates_checked_at",
		"update_check_warnings,omitempty",
	} {
		if !summaryFields[want] {
			t.Errorf("SystemSummary is missing the %q json field", want)
		}
	}
}

// The three fakes below stand in for the NATS connection.
//
// They are mutex-guarded because a group action calls them from one goroutine
// per member at once; without it `go test -race` fails on the call counter
// rather than on anything the production code did wrong.
//
// perHost lets one test produce accepted, skipped and failed answers in a
// single batch, which is the case worth pinning — a group where every host
// agrees proves very little.

// fakeUpdater stands in for the NATS connection in the update-request tests.
type fakeUpdater struct {
	mu       sync.Mutex
	ack      models.UpdateAck
	err      error
	perHost  map[string]fakeAnswer
	hostname string // recorded from the last call
	by       string
	calls    int
	seen     []string
	hook     func(hostname string)
}

// fakeAnswer is one host's scripted reply, for the per-host maps.
type fakeAnswer struct {
	accepted bool
	reason   string
	id       string
	command  string
	err      error
}

func (f *fakeUpdater) RequestUpdate(hostname, requestedBy string) (models.UpdateAck, error) {
	if f.hook != nil {
		f.hook(hostname)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.hostname = hostname
	f.by = requestedBy
	f.seen = append(f.seen, hostname)
	if a, ok := f.perHost[hostname]; ok {
		return models.UpdateAck{ID: a.id, Hostname: hostname, Accepted: a.accepted, Reason: a.reason, Command: a.command}, a.err
	}
	return f.ack, f.err
}

func (f *fakeUpdater) hostsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func (f *fakeUpdater) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeCheckIner stands in for the NATS connection in the check-in tests.
type fakeCheckIner struct {
	mu       sync.Mutex
	ack      models.CheckInAck
	err      error
	perHost  map[string]fakeAnswer
	hostname string // recorded from the last call
	by       string
	calls    int
}

func (f *fakeCheckIner) RequestCheckIn(hostname, requestedBy string) (models.CheckInAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.hostname = hostname
	f.by = requestedBy
	if a, ok := f.perHost[hostname]; ok {
		return models.CheckInAck{ID: a.id, Hostname: hostname, Accepted: a.accepted, Reason: a.reason}, a.err
	}
	return f.ack, f.err
}

func (f *fakeCheckIner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeRebooter stands in for the NATS connection in the reboot tests.
type fakeRebooter struct {
	mu       sync.Mutex
	ack      models.RebootAck
	err      error
	perHost  map[string]fakeAnswer
	hostname string // recorded from the last call
	by       string
	calls    int
}

func (f *fakeRebooter) RequestReboot(hostname, requestedBy string) (models.RebootAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.hostname = hostname
	f.by = requestedBy
	if a, ok := f.perHost[hostname]; ok {
		return models.RebootAck{ID: a.id, Hostname: hostname, Accepted: a.accepted, Reason: a.reason}, a.err
	}
	return f.ack, f.err
}

func (f *fakeRebooter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func postReboot(handler http.HandlerFunc, hostname string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/systems/"+hostname+"/reboot", nil)
	req = mux.SetURLVars(req, map[string]string{"hostname": hostname})
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func postCheckIn(handler http.HandlerFunc, hostname string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/systems/"+hostname+"/checkin", nil)
	req = mux.SetURLVars(req, map[string]string{"hostname": hostname})
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func postUpdate(handler http.HandlerFunc, hostname string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/systems/"+hostname+"/update", nil)
	req = mux.SetURLVars(req, map[string]string{"hostname": hostname})
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// TestRunUpdateHandlerDisabled pins the server-side half of the opt-in: with no
// updater wired in, the route exists but refuses, and nothing is dispatched.
func TestRunUpdateHandlerDisabled(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}

	rec := postUpdate(RunUpdateHandler(store, nil), "smallboi")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// TestRunUpdateHandlerHostNotOptedIn is the client-side half: a host that has
// not opted in is refused before any command goes out, so the dashboard can say
// why rather than waiting out a request nobody answers.
func TestRunUpdateHandlerHostNotOptedIn(t *testing.T) {
	system := fullySetSystem()
	system.RemoteUpdatesEnabled = false
	store := &fakeStorage{systems: []models.System{system}}
	updater := &fakeUpdater{ack: models.UpdateAck{Accepted: true}}

	rec := postUpdate(RunUpdateHandler(store, updater), "smallboi")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if updater.calls != 0 {
		t.Errorf("dispatched %d update requests for a host that has not opted in, want 0", updater.calls)
	}
}

func TestRunUpdateHandlerUnknownHost(t *testing.T) {
	store := &fakeStorage{}
	updater := &fakeUpdater{ack: models.UpdateAck{Accepted: true}}

	rec := postUpdate(RunUpdateHandler(store, updater), "nosuchhost")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if updater.calls != 0 {
		t.Errorf("dispatched %d update requests for an unknown host, want 0", updater.calls)
	}
}

func TestRunUpdateHandlerAccepted(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}
	updater := &fakeUpdater{ack: models.UpdateAck{ID: "abc123", Accepted: true, Command: "/usr/libexec/muc/upd"}}

	rec := postUpdate(RunUpdateHandler(store, updater), "smallboi")

	// 202, not 200: the run has started, and its result arrives later over NATS.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if updater.hostname != "smallboi" {
		t.Errorf("dispatched to %q, want %q", updater.hostname, "smallboi")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["id"] != "abc123" {
		t.Errorf("id = %q, want %q — the dashboard needs the host's own run id", body["id"], "abc123")
	}
}

// TestRunUpdateHandlerRefused covers the host answering "busy": that is a
// refusal to report, not an error to swallow.
func TestRunUpdateHandlerRefused(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}
	updater := &fakeUpdater{ack: models.UpdateAck{Accepted: false, Reason: "an update is already running on this host"}}

	rec := postUpdate(RunUpdateHandler(store, updater), "smallboi")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already running") {
		t.Errorf("body = %q, want the host's own reason", rec.Body.String())
	}
}

// TestRunUpdateHandlerNotListening distinguishes "nobody answered" from a
// server-side failure: the host is simply offline or no longer accepting.
func TestRunUpdateHandlerNotListening(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}
	updater := &fakeUpdater{err: models.ErrHostNotListening}

	rec := postUpdate(RunUpdateHandler(store, updater), "smallboi")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// TestFeaturesHandler pins what the dashboard reads to decide whether to draw
// the update button at all.
func TestFeaturesHandler(t *testing.T) {
	for _, tc := range []struct {
		name        string
		updater     UpdateRequester
		rebooter    RebootRequester
		wantUpdates bool
		wantReboot  bool
	}{
		{"disabled", nil, nil, false, false},
		{"updates only", &fakeUpdater{}, nil, true, false},
		{"reboot only", nil, &fakeRebooter{}, false, true},
		{"both", &fakeUpdater{}, &fakeRebooter{}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			FeaturesHandler(tc.updater, tc.rebooter)(rec, httptest.NewRequest(http.MethodGet, "/api/features", nil))

			var got map[string]bool
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if got["remote_updates"] != tc.wantUpdates {
				t.Errorf("remote_updates = %v, want %v", got["remote_updates"], tc.wantUpdates)
			}
			if got["remote_reboot"] != tc.wantReboot {
				t.Errorf("remote_reboot = %v, want %v", got["remote_reboot"], tc.wantReboot)
			}
		})
	}
}

// TestCheckInHandlerAccepted pins the ungated path: no server flag and no host
// opt-in is consulted, unlike the update route — any known host can be asked.
// 202, not 200: the check-in itself arrives later, over NATS.
func TestCheckInHandlerAccepted(t *testing.T) {
	system := fullySetSystem()
	system.RemoteUpdatesEnabled = false // deliberately: it has no bearing here
	store := &fakeStorage{systems: []models.System{system}}
	checkiner := &fakeCheckIner{ack: models.CheckInAck{ID: "abc123", Accepted: true}}

	rec := postCheckIn(CheckInHandler(store, checkiner), "smallboi")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if checkiner.hostname != "smallboi" {
		t.Errorf("dispatched to %q, want %q", checkiner.hostname, "smallboi")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["id"] != "abc123" {
		t.Errorf("id = %q, want the host's own request id %q", body["id"], "abc123")
	}
}

func TestCheckInHandlerUnknownHost(t *testing.T) {
	store := &fakeStorage{}
	checkiner := &fakeCheckIner{ack: models.CheckInAck{Accepted: true}}

	rec := postCheckIn(CheckInHandler(store, checkiner), "nosuchhost")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if checkiner.calls != 0 {
		t.Errorf("dispatched %d check-in requests for an unknown host, want 0", checkiner.calls)
	}
}

// TestCheckInHandlerRefused carries the host's rate-limit refusal through as the
// reason, since "try again in 7s" is the whole content of the answer.
func TestCheckInHandlerRefused(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}
	checkiner := &fakeCheckIner{ack: models.CheckInAck{
		Reason: "this host checked in less than 10s ago; try again in 7s",
	}}

	rec := postCheckIn(CheckInHandler(store, checkiner), "smallboi")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "try again in 7s") {
		t.Errorf("body = %q, want the host's own reason", rec.Body.String())
	}
}

// TestCheckInHandlerNotListening covers a host that is offline or running a
// client from before the command existed: not a server-side failure.
func TestCheckInHandlerNotListening(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}
	checkiner := &fakeCheckIner{err: models.ErrHostNotListening}

	rec := postCheckIn(CheckInHandler(store, checkiner), "smallboi")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// TestCheckInHandlerWithoutNATS is the only way this route is unavailable: there
// is no configuration that turns it off, only the absence of a connection to
// send the command over.
func TestCheckInHandlerWithoutNATS(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}

	rec := postCheckIn(CheckInHandler(store, nil), "smallboi")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// TestRebootHandlerDisabled pins the server-side half of the reboot opt-in: no
// rebooter wired in means the route refuses and dispatches nothing.
func TestRebootHandlerDisabled(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}

	rec := postReboot(RebootHandler(store, nil), "smallboi")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// TestRebootHandlerHostNotOptedIn: the reboot opt-in is its own flag, so a host
// that allows updates but not reboots is refused here.
func TestRebootHandlerHostNotOptedIn(t *testing.T) {
	system := fullySetSystem()
	system.RemoteUpdatesEnabled = true
	system.RemoteRebootEnabled = false
	store := &fakeStorage{systems: []models.System{system}}
	rebooter := &fakeRebooter{ack: models.RebootAck{Accepted: true}}

	rec := postReboot(RebootHandler(store, rebooter), "smallboi")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if rebooter.calls != 0 {
		t.Errorf("dispatched %d reboot requests to a host that has not opted in, want 0", rebooter.calls)
	}
	if !strings.Contains(rec.Body.String(), "allow_remote_reboot") {
		t.Errorf("body = %q, want it to name the client setting", rec.Body.String())
	}
}

// TestRebootHandlerNoRebootPending: the dashboard only enables the button for a
// host reporting reboot_required, and the route holds the same line.
func TestRebootHandlerNoRebootPending(t *testing.T) {
	system := fullySetSystem()
	system.RebootRequired = false
	store := &fakeStorage{systems: []models.System{system}}
	rebooter := &fakeRebooter{ack: models.RebootAck{Accepted: true}}

	rec := postReboot(RebootHandler(store, rebooter), "smallboi")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if rebooter.calls != 0 {
		t.Errorf("dispatched %d reboot requests to a host with no reboot pending, want 0", rebooter.calls)
	}
}

func TestRebootHandlerUnknownHost(t *testing.T) {
	store := &fakeStorage{}
	rebooter := &fakeRebooter{ack: models.RebootAck{Accepted: true}}

	rec := postReboot(RebootHandler(store, rebooter), "nosuchhost")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rebooter.calls != 0 {
		t.Errorf("dispatched %d reboot requests for an unknown host, want 0", rebooter.calls)
	}
}

func TestRebootHandlerAccepted(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}
	rebooter := &fakeRebooter{ack: models.RebootAck{ID: "abc123", Accepted: true}}

	rec := postReboot(RebootHandler(store, rebooter), "smallboi")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if rebooter.hostname != "smallboi" {
		t.Errorf("dispatched to %q, want %q", rebooter.hostname, "smallboi")
	}
	if rebooter.by == "" {
		t.Error("requested_by not passed through; the host's journal should say who asked")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["id"] != "abc123" {
		t.Errorf("id = %q, want the host's own request id %q", body["id"], "abc123")
	}
}

// TestRebootHandlerRefused carries the host's own reason through: it is the
// host that knows an update is running or that no reboot is pending any more.
func TestRebootHandlerRefused(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}
	rebooter := &fakeRebooter{ack: models.RebootAck{
		Reason: "an update is running on this host; reboot it once the run has finished",
	}}

	rec := postReboot(RebootHandler(store, rebooter), "smallboi")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "an update is running") {
		t.Errorf("body = %q, want the host's own reason", rec.Body.String())
	}
}

func TestRebootHandlerNotListening(t *testing.T) {
	store := &fakeStorage{systems: []models.System{fullySetSystem()}}
	rebooter := &fakeRebooter{err: models.ErrHostNotListening}

	rec := postReboot(RebootHandler(store, rebooter), "smallboi")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
