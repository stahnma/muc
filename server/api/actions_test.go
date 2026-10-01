package api

import (
	"net/http"
	"server/models"
	"slices"
	"sync"
	"testing"
	"time"
)

// optedInSystem returns a host that would accept any of the three commands, so
// a test only has to spell out the thing it is taking away.
func optedInSystem(hostname string) models.System {
	return models.System{
		Hostname:             hostname,
		RemoteUpdatesEnabled: true,
		RemoteRebootEnabled:  true,
		RebootRequired:       true,
	}
}

// TestDispatchStatusMatchesSingleHostRoutes is the test this whole refactor
// exists for.
//
// The group routes classify a member by the status the single-host route would
// have returned for it. If the two ever disagree, the dashboard offers a button
// for one host and silently does nothing for that same host inside a group —
// a failure nobody would notice until a patch window went by with a machine
// left behind. Here the same setup is driven down both paths and the statuses
// are required to match.
func TestDispatchStatusMatchesSingleHostRoutes(t *testing.T) {
	notListening := models.ErrHostNotListening

	for _, tc := range []struct {
		name   string
		system models.System
		ack    any
		err    error
		want   int
	}{
		{"accepted", optedInSystem("smallboi"), true, nil, http.StatusAccepted},
		{"host refused", optedInSystem("smallboi"), false, nil, http.StatusConflict},
		{"not listening", optedInSystem("smallboi"), true, notListening, http.StatusServiceUnavailable},
		{"request failed", optedInSystem("smallboi"), true, errNotFound{}, http.StatusBadGateway},
	} {
		for _, act := range []action{actionUpdate, actionCheckIn, actionReboot} {
			t.Run(string(act)+"/"+tc.name, func(t *testing.T) {
				store := &fakeStorage{systems: []models.System{tc.system}}
				accepted := tc.ack.(bool)

				var (
					got      int
					viaRoute int
				)
				switch act {
				case actionUpdate:
					f := &fakeUpdater{ack: models.UpdateAck{Accepted: accepted, ID: "abc"}, err: tc.err}
					got = statusFor(dispatch(store, act, requesters{update: f}, "smallboi", "tester"))
					viaRoute = postUpdate(RunUpdateHandler(store, f), "smallboi").Code
				case actionCheckIn:
					f := &fakeCheckIner{ack: models.CheckInAck{Accepted: accepted, ID: "abc"}, err: tc.err}
					got = statusFor(dispatch(store, act, requesters{checkIn: f}, "smallboi", "tester"))
					viaRoute = postCheckIn(CheckInHandler(store, f), "smallboi").Code
				case actionReboot:
					f := &fakeRebooter{ack: models.RebootAck{Accepted: accepted, ID: "abc"}, err: tc.err}
					got = statusFor(dispatch(store, act, requesters{reboot: f}, "smallboi", "tester"))
					viaRoute = postReboot(RebootHandler(store, f), "smallboi").Code
				}

				if got != tc.want {
					t.Errorf("statusFor(dispatch) = %d, want %d", got, tc.want)
				}
				if viaRoute != got {
					t.Errorf("the single-host route returned %d but dispatch classifies it as %d; "+
						"the group path and the per-host path have drifted apart", viaRoute, got)
				}
			})
		}
	}
}

// TestDispatchSkipsWhatTheRouteRefuses covers the rungs that are specific to
// one action: the opt-ins and the pending-reboot precondition. These are the
// ones a group has to skip rather than fail on.
func TestDispatchSkipsWhatTheRouteRefuses(t *testing.T) {
	noUpdates := optedInSystem("smallboi")
	noUpdates.RemoteUpdatesEnabled = false

	noReboots := optedInSystem("smallboi")
	noReboots.RemoteRebootEnabled = false

	noPending := optedInSystem("smallboi")
	noPending.RebootRequired = false

	for _, tc := range []struct {
		name       string
		act        action
		system     models.System
		wantCode   string
		wantStatus int
	}{
		{"update, host not opted in", actionUpdate, noUpdates, CodeNotOptedIn, http.StatusConflict},
		{"reboot, host not opted in", actionReboot, noReboots, CodeNotOptedIn, http.StatusConflict},
		{"reboot, nothing pending", actionReboot, noPending, CodeNoRebootPending, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStorage{systems: []models.System{tc.system}}
			updater := &fakeUpdater{ack: models.UpdateAck{Accepted: true}}
			rebooter := &fakeRebooter{ack: models.RebootAck{Accepted: true}}

			res := dispatch(store, tc.act, requesters{update: updater, reboot: rebooter}, "smallboi", "tester")

			if res.Outcome != OutcomeSkipped {
				t.Errorf("Outcome = %q, want %q", res.Outcome, OutcomeSkipped)
			}
			if res.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", res.Code, tc.wantCode)
			}
			if got := statusFor(res); got != tc.wantStatus {
				t.Errorf("statusFor = %d, want %d", got, tc.wantStatus)
			}
			// Nothing was sent: a skip is decided before the host is bothered.
			if updater.callCount()+rebooter.callCount() != 0 {
				t.Error("a skipped host was still sent a command")
			}
		})
	}
}

// TestDispatchUnknownHostIsSkippedNotFailed: a group may name a host the server
// has never seen — a group built before its hosts exist, or one whose host was
// deleted from the dashboard. That is a legitimate state, so it is a skip, and
// nothing is sent.
func TestDispatchUnknownHostIsSkipped(t *testing.T) {
	store := &fakeStorage{}
	updater := &fakeUpdater{ack: models.UpdateAck{Accepted: true}}

	res := dispatch(store, actionUpdate, requesters{update: updater}, "ghost", "tester")

	if res.Outcome != OutcomeSkipped || res.Code != CodeUnknownHost {
		t.Errorf("got %q/%q, want %q/%q", res.Outcome, res.Code, OutcomeSkipped, CodeUnknownHost)
	}
	if statusFor(res) != http.StatusNotFound {
		t.Errorf("statusFor = %d, want 404", statusFor(res))
	}
	if updater.callCount() != 0 {
		t.Error("a host the server does not know was still sent a command")
	}
}

// TestDispatchRefusalReasonFallsBackPerAction pins the default sentences. A
// host that refuses without saying why still has to produce something the
// operator can read, and the wording differs by action on purpose.
func TestDispatchRefusalReasonFallsBackPerAction(t *testing.T) {
	store := &fakeStorage{systems: []models.System{optedInSystem("smallboi")}}

	for _, tc := range []struct {
		act  action
		want string
	}{
		{actionUpdate, "the host refused the update request"},
		{actionReboot, "the host refused the reboot request"},
		{actionCheckIn, "the host refused the check-in request"},
	} {
		t.Run(string(tc.act), func(t *testing.T) {
			req := requesters{
				update:  &fakeUpdater{ack: models.UpdateAck{Accepted: false}},
				reboot:  &fakeRebooter{ack: models.RebootAck{Accepted: false}},
				checkIn: &fakeCheckIner{ack: models.CheckInAck{Accepted: false}},
			}
			res := dispatch(store, tc.act, req, "smallboi", "tester")
			if res.Reason != tc.want {
				t.Errorf("Reason = %q, want %q", res.Reason, tc.want)
			}
		})
	}
}

// TestDispatchKeepsTheHostsOwnReason: when the host says why, that is what the
// operator sees — "an update is already running on this host" is far more use
// than a generic refusal.
func TestDispatchKeepsTheHostsOwnReason(t *testing.T) {
	store := &fakeStorage{systems: []models.System{optedInSystem("smallboi")}}
	updater := &fakeUpdater{ack: models.UpdateAck{Accepted: false, Reason: "an update is already running on this host"}}

	res := dispatch(store, actionUpdate, requesters{update: updater}, "smallboi", "tester")

	if res.Reason != "an update is already running on this host" {
		t.Errorf("Reason = %q, want the host's own reason", res.Reason)
	}
}

// TestDispatchGroupPreservesMemberOrder: results are read as a list by a human,
// so they come back in the group's order however the goroutines finished. The
// scripted delays here are deliberately inverted against the member order.
func TestDispatchGroupPreservesMemberOrder(t *testing.T) {
	members := []string{"a", "b", "c", "d"}
	systems := make([]models.System, 0, len(members))
	for _, m := range members {
		systems = append(systems, optedInSystem(m))
	}
	store := &fakeStorage{systems: systems}

	delays := map[string]time.Duration{"a": 40 * time.Millisecond, "b": 30 * time.Millisecond, "c": 20 * time.Millisecond, "d": 0}
	updater := &fakeUpdater{
		ack:  models.UpdateAck{Accepted: true},
		hook: func(hostname string) { time.Sleep(delays[hostname]) },
	}

	results := dispatchGroup(store, actionUpdate, requesters{update: updater}, members, "tester")

	if len(results) != len(members) {
		t.Fatalf("got %d results, want %d", len(results), len(members))
	}
	for i, m := range members {
		if results[i].Hostname != m {
			t.Errorf("results[%d].Hostname = %q, want %q", i, results[i].Hostname, m)
		}
	}
}

// TestDispatchGroupRunsConcurrently pins the "all at once" decision in a way
// that cannot silently regress: every member blocks until all of them have
// arrived. A serialised implementation never gets past the first one and the
// test times out rather than passing slowly.
func TestDispatchGroupRunsConcurrently(t *testing.T) {
	members := []string{"a", "b", "c", "d", "e"}
	systems := make([]models.System, 0, len(members))
	for _, m := range members {
		systems = append(systems, optedInSystem(m))
	}
	store := &fakeStorage{systems: systems}

	var (
		mu      sync.Mutex
		arrived int
	)
	allHere := make(chan struct{})
	updater := &fakeUpdater{
		ack: models.UpdateAck{Accepted: true},
		hook: func(string) {
			mu.Lock()
			arrived++
			if arrived == len(members) {
				close(allHere)
			}
			mu.Unlock()
			<-allHere
		},
	}

	done := make(chan []HostActionResult, 1)
	go func() {
		done <- dispatchGroup(store, actionUpdate, requesters{update: updater}, members, "tester")
	}()

	select {
	case results := <-done:
		for _, res := range results {
			if res.Outcome != OutcomeAccepted {
				t.Errorf("%s: outcome = %q, want %q", res.Hostname, res.Outcome, OutcomeAccepted)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchGroup did not finish: the members are not being asked at the same time")
	}
}

// TestDispatchGroupMixedOutcomes is the case worth pinning — a group where
// every host agrees proves very little. One host accepts, one was never a
// candidate, one is offline, and the batch still runs for the rest.
func TestDispatchGroupMixedOutcomes(t *testing.T) {
	optedOut := optedInSystem("db01")
	optedOut.RemoteUpdatesEnabled = false

	store := &fakeStorage{systems: []models.System{
		optedInSystem("web01"),
		optedOut,
		optedInSystem("web02"),
	}}

	updater := &fakeUpdater{
		perHost: map[string]fakeAnswer{
			"web01": {accepted: true, id: "run-1", command: "/usr/libexec/muc/upd"},
			"web02": {err: models.ErrHostNotListening},
		},
	}

	members := []string{"web01", "db01", "web02", "ghost01"}
	results := dispatchGroup(store, actionUpdate, requesters{update: updater}, members, "tester")

	want := []struct {
		outcome string
		code    string
	}{
		{OutcomeAccepted, ""},
		{OutcomeSkipped, CodeNotOptedIn},
		{OutcomeUnreachable, CodeNotListening},
		{OutcomeSkipped, CodeUnknownHost},
	}
	for i, w := range want {
		if results[i].Outcome != w.outcome || results[i].Code != w.code {
			t.Errorf("results[%d] (%s) = %q/%q, want %q/%q",
				i, members[i], results[i].Outcome, results[i].Code, w.outcome, w.code)
		}
	}

	// Only the two candidates were actually asked. The opted-out host and the
	// unknown one must not reach NATS at all.
	seen := updater.hostsSeen()
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"web01", "web02"}) {
		t.Errorf("hosts asked = %v, want [web01 web02]", seen)
	}
}

// TestDispatchGroupOnNoMembers: an empty group is a legitimate state, not an
// error, and must produce an empty list rather than a nil one — the dashboard
// iterates it without checking.
func TestDispatchGroupOnNoMembers(t *testing.T) {
	store := &fakeStorage{}
	updater := &fakeUpdater{}

	results := dispatchGroup(store, actionUpdate, requesters{update: updater}, nil, "tester")

	if results == nil {
		t.Fatal("results is nil, want an empty slice")
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}
