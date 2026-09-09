package nats

import (
	"encoding/json"
	"server/models"
	"testing"

	nats "github.com/nats-io/nats.go"
)

// memStore is a minimal in-memory Storage for driving the message handlers
// directly, without a NATS connection.
type memStore struct {
	systems map[string]models.System
	saves   int
}

func newMemStore(systems ...models.System) *memStore {
	s := &memStore{systems: map[string]models.System{}}
	for _, system := range systems {
		s.systems[system.Hostname] = system
	}
	return s
}

func (s *memStore) SaveSystem(hostname string, system models.System) error {
	s.saves++
	s.systems[hostname] = system
	return nil
}

func (s *memStore) GetSystem(hostname string) (models.System, error) {
	system, ok := s.systems[hostname]
	if !ok {
		return models.System{}, errMissing{}
	}
	return system, nil
}

func (s *memStore) GetAllSystems() ([]models.System, error) {
	all := make([]models.System, 0, len(s.systems))
	for _, system := range s.systems {
		all = append(all, system)
	}
	return all, nil
}

func (s *memStore) DeleteSystem(hostname string) error {
	delete(s.systems, hostname)
	return nil
}

func (s *memStore) SubscribeToUpdates() <-chan models.System { return make(chan models.System) }

type errMissing struct{}

func (errMissing) Error() string { return "not found" }

func msg(t *testing.T, subject string, payload any) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &nats.Msg{Subject: subject, Data: data}
}

// TestUpdateResultHandlerRecordsRun covers the path that puts a run on the
// dashboard: the result arrives on its own subject and has to be attached to
// the host named by that subject.
func TestUpdateResultHandlerRecordsRun(t *testing.T) {
	store := newMemStore(models.System{Hostname: "smallboi"})
	run := models.UpdateRun{ID: "abc", Status: "succeeded", StartedAt: "2026-07-31T23:38:02Z"}

	updateResultHandler(store)(msg(t, "systems.results.update.smallboi", run))

	got := store.systems["smallboi"].LastUpdateRun
	if got == nil {
		t.Fatal("LastUpdateRun is nil; the run was not recorded")
	}
	if got.ID != "abc" || got.Status != "succeeded" {
		t.Errorf("LastUpdateRun = %+v, want the published run", got)
	}
}

// TestUpdateResultHandlerUsesTheSubjectHostname pins that the payload cannot
// claim to be about another host: the subject is the only authority on which
// system a result belongs to.
func TestUpdateResultHandlerUsesTheSubjectHostname(t *testing.T) {
	store := newMemStore(models.System{Hostname: "smallboi"}, models.System{Hostname: "bigboi"})

	updateResultHandler(store)(msg(t, "systems.results.update.smallboi", map[string]string{
		"id": "abc", "status": "succeeded", "hostname": "bigboi",
	}))

	if store.systems["bigboi"].LastUpdateRun != nil {
		t.Error("a result published for smallboi was recorded against bigboi")
	}
	if store.systems["smallboi"].LastUpdateRun == nil {
		t.Error("the result was not recorded against the host that published it")
	}
}

// TestUpdateResultHandlerUnknownHost checks an unknown host is dropped rather
// than conjuring a system record with nothing in it but an update run.
func TestUpdateResultHandlerUnknownHost(t *testing.T) {
	store := newMemStore()

	updateResultHandler(store)(msg(t, "systems.results.update.ghost", models.UpdateRun{ID: "abc"}))

	if store.saves != 0 {
		t.Errorf("saved %d systems for a host that has never checked in, want 0", store.saves)
	}
}

// TestCheckInKeepsTheLastUpdateRun is the reason the carry-forward exists: the
// client does not report the run in its check-in, so without this the record of
// an update would vanish within five minutes of the run finishing.
func TestCheckInKeepsTheLastUpdateRun(t *testing.T) {
	previous := models.System{
		Hostname:      "smallboi",
		LastUpdateRun: &models.UpdateRun{ID: "abc", Status: "succeeded"},
	}
	store := newMemStore(previous)

	checkInHandler(store)(msg(t, "systems.updates.smallboi", models.System{
		Hostname:         "smallboi",
		UpdatesAvailable: true,
	}))

	got := store.systems["smallboi"]
	if got.LastUpdateRun == nil {
		t.Fatal("LastUpdateRun was dropped by an ordinary check-in")
	}
	if got.LastUpdateRun.ID != "abc" {
		t.Errorf("LastUpdateRun.ID = %q, want the previous run's %q", got.LastUpdateRun.ID, "abc")
	}
	if !got.UpdatesAvailable {
		t.Error("the check-in's own fields were not saved")
	}
}

// TestCheckInCarriesTheOptIn guards the field the dashboard keys its button on:
// it comes from the client on every check-in, so a client that stops reporting
// it must stop offering the button too.
func TestCheckInCarriesTheOptIn(t *testing.T) {
	store := newMemStore(models.System{Hostname: "smallboi", RemoteUpdatesEnabled: true})

	checkInHandler(store)(msg(t, "systems.updates.smallboi", models.System{
		Hostname:             "smallboi",
		RemoteUpdatesEnabled: false,
	}))

	if store.systems["smallboi"].RemoteUpdatesEnabled {
		t.Error("RemoteUpdatesEnabled stayed true after the client stopped reporting it")
	}
}
