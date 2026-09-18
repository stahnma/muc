package main

import (
	"errors"
	"strings"
	"testing"
)

// newTestRebootListener returns a listener whose every input is under the
// test's control, and that records rather than reboots.
func newTestRebootListener() (*rebootListener, *[]rebootRecord) {
	var published []rebootRecord
	l := &rebootListener{
		hostname:       "smallboi",
		updateRunning:  func() bool { return false },
		rebootRequired: func() bool { return true },
		reboot:         func() (string, error) { return "/usr/bin/systemctl reboot", nil },
		publish:        func(r rebootRecord) { published = append(published, r) },
	}
	return l, &published
}

func TestRebootDecideAccepts(t *testing.T) {
	l, _ := newTestRebootListener()

	ack := l.decide(rebootRequest{ID: "abc123", RequestedBy: "192.168.1.20"})

	if !ack.Accepted {
		t.Fatalf("refused: %q", ack.Reason)
	}
	if ack.ID != "abc123" || ack.Hostname != "smallboi" {
		t.Errorf("ack = %+v, want the request's id and this host's name", ack)
	}
}

// TestRebootDecideRefusesWithoutAPendingReboot is the check that does not depend
// on trusting the caller: the dashboard's button is drawn from an old check-in,
// and the host is the one that knows whether it still applies.
func TestRebootDecideRefusesWithoutAPendingReboot(t *testing.T) {
	l, _ := newTestRebootListener()
	l.rebootRequired = func() bool { return false }

	ack := l.decide(rebootRequest{ID: "abc123"})

	if ack.Accepted {
		t.Fatal("accepted a reboot the host does not need")
	}
	if !strings.Contains(ack.Reason, "does not currently need a reboot") {
		t.Errorf("reason = %q, want it to say no reboot is pending", ack.Reason)
	}
}

func TestRebootDecideRefusesDuringAnUpdate(t *testing.T) {
	l, _ := newTestRebootListener()
	l.updateRunning = func() bool { return true }

	ack := l.decide(rebootRequest{ID: "abc123"})

	if ack.Accepted {
		t.Fatal("accepted a reboot while an update run was in flight")
	}
	if !strings.Contains(ack.Reason, "update is running") {
		t.Errorf("reason = %q, want it to blame the running update", ack.Reason)
	}
}

// TestRebootDecideRefusesASecondRequest: a reboot that has been accepted holds
// the slot for good on success, since nothing releases it before the host goes
// down.
func TestRebootDecideRefusesASecondRequest(t *testing.T) {
	l, _ := newTestRebootListener()

	if ack := l.decide(rebootRequest{ID: "first"}); !ack.Accepted {
		t.Fatalf("first request refused: %q", ack.Reason)
	}
	ack := l.decide(rebootRequest{ID: "second"})

	if ack.Accepted {
		t.Fatal("accepted a second reboot while the first was in progress")
	}
	if !strings.Contains(ack.Reason, "already in progress") {
		t.Errorf("reason = %q, want it to say a reboot is already in progress", ack.Reason)
	}
}

// TestRebootExecutePublishesBeforeRebooting pins the ordering the dashboard
// depends on: the record goes out before the command that ends the process.
func TestRebootExecutePublishesBeforeRebooting(t *testing.T) {
	l, published := newTestRebootListener()
	var publishedBeforeReboot int
	l.reboot = func() (string, error) {
		publishedBeforeReboot = len(*published)
		return "/usr/bin/systemctl reboot", nil
	}
	l.decide(rebootRequest{ID: "abc123"})

	l.execute(rebootRequest{ID: "abc123", RequestedAt: "2026-09-18T12:00:00Z", RequestedBy: "192.168.1.20"})

	if publishedBeforeReboot != 1 {
		t.Fatalf("%d records published before the reboot command ran, want 1", publishedBeforeReboot)
	}
	got := (*published)[0]
	if got.Status != rebootStatusRebooting {
		t.Errorf("status = %q, want %q", got.Status, rebootStatusRebooting)
	}
	if got.ID != "abc123" || got.RequestedBy != "192.168.1.20" || got.RequestedAt != "2026-09-18T12:00:00Z" {
		t.Errorf("record = %+v, want the request's id, requester and time", got)
	}
	if len(*published) != 1 {
		t.Errorf("%d records published in all, want 1: a successful reboot has nothing more to say", len(*published))
	}
	if !l.inProgress {
		t.Error("the slot was released after a reboot command that succeeded")
	}
}

// TestRebootExecuteReportsAFailedCommand: a reboot command that fails is the
// one outcome the host can still report, so it does, and frees the slot for
// another try.
func TestRebootExecuteReportsAFailedCommand(t *testing.T) {
	l, published := newTestRebootListener()
	l.reboot = func() (string, error) {
		return "/usr/bin/systemctl reboot", errors.New("Failed to set wall message: access denied")
	}
	l.decide(rebootRequest{ID: "abc123"})

	l.execute(rebootRequest{ID: "abc123"})

	if len(*published) != 2 {
		t.Fatalf("%d records published, want 2 (rebooting, then failed)", len(*published))
	}
	got := (*published)[1]
	if got.Status != rebootStatusFailed {
		t.Errorf("status = %q, want %q", got.Status, rebootStatusFailed)
	}
	if !strings.Contains(got.Error, "access denied") {
		t.Errorf("error = %q, want the command's own message", got.Error)
	}
	if got.Command != "/usr/bin/systemctl reboot" {
		t.Errorf("command = %q, want the command that was tried", got.Command)
	}
	if got.FinishedAt == "" || got.RequestedAt == "" {
		t.Errorf("record = %+v, want both timestamps set", got)
	}
	if l.inProgress {
		t.Error("the slot stayed claimed after the reboot command failed")
	}
}
