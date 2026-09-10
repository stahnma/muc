package main

import (
	"strings"
	"testing"
	"time"
)

// newTestListener returns a listener with a clock the test drives and a channel
// deep enough to see what was asked for.
func newTestListener(now *time.Time) (*checkInListener, chan struct{}) {
	recheck := make(chan struct{}, 1)
	return &checkInListener{
		hostname:    "smallboi",
		recheck:     recheck,
		now:         func() time.Time { return *now },
		minInterval: minCheckInInterval,
	}, recheck
}

func TestCheckInDecideAcceptsAndAsksForACheck(t *testing.T) {
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	l, recheck := newTestListener(&clock)

	ack := l.decide(checkInRequest{ID: "abc123", RequestedBy: "192.168.1.20"})

	if !ack.Accepted {
		t.Fatalf("first command refused: %q", ack.Reason)
	}
	if ack.ID != "abc123" || ack.Hostname != "smallboi" {
		t.Errorf("ack = %+v, want the request's id and this host's name", ack)
	}
	select {
	case <-recheck:
	default:
		t.Error("accepted the command without asking the main loop for a check-in")
	}
}

// TestCheckInDecideRefusesInsideTheInterval is the guard that makes an ungated
// command subject acceptable: a caller cannot turn the button into a way to hold
// a host at a continuous package-manager refresh.
func TestCheckInDecideRefusesInsideTheInterval(t *testing.T) {
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	l, recheck := newTestListener(&clock)

	l.decide(checkInRequest{ID: "first"})
	<-recheck // take the first request, so a second one would be visible

	clock = clock.Add(3 * time.Second)
	ack := l.decide(checkInRequest{ID: "second"})

	if ack.Accepted {
		t.Fatal("accepted a second command 3s after the first")
	}
	if !strings.Contains(ack.Reason, "7s") {
		t.Errorf("reason = %q, want it to say how long to wait (7s)", ack.Reason)
	}
	select {
	case <-recheck:
		t.Error("a refused command still asked for a check-in")
	default:
	}
}

func TestCheckInDecideAcceptsAfterTheInterval(t *testing.T) {
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	l, recheck := newTestListener(&clock)

	l.decide(checkInRequest{ID: "first"})
	<-recheck

	clock = clock.Add(minCheckInInterval)
	if ack := l.decide(checkInRequest{ID: "second"}); !ack.Accepted {
		t.Fatalf("refused a command exactly %s after the last: %q", minCheckInInterval, ack.Reason)
	}
	select {
	case <-recheck:
	default:
		t.Error("accepted the command without asking the main loop for a check-in")
	}
}

// TestCheckInDecideCoalescesWithAPendingCheck covers the send being
// non-blocking: with a check already queued and nobody yet reading it, a fresh
// command must still be answered rather than deadlocking the subscription
// goroutine, since the queued check is the one it is asking for.
func TestCheckInDecideCoalescesWithAPendingCheck(t *testing.T) {
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	l, recheck := newTestListener(&clock)

	l.decide(checkInRequest{ID: "first"}) // left unread in the channel

	clock = clock.Add(minCheckInInterval)
	done := make(chan checkInAck, 1)
	go func() { done <- l.decide(checkInRequest{ID: "second"}) }()

	select {
	case ack := <-done:
		if !ack.Accepted {
			t.Fatalf("refused: %q", ack.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("decide blocked on a full recheck channel")
	}
	if len(recheck) != 1 {
		t.Errorf("recheck holds %d requests, want the two coalesced into 1", len(recheck))
	}
}

func TestCeilSeconds(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want time.Duration
	}{
		{10 * time.Millisecond, time.Second}, // never "try again in 0s"
		{time.Second, time.Second},
		{1500 * time.Millisecond, 2 * time.Second},
	} {
		if got := ceilSeconds(tc.in); got != tc.want {
			t.Errorf("ceilSeconds(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
