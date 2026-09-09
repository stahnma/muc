package main

import (
	"testing"
	"time"
)

func TestSettleTimerStartsIdle(t *testing.T) {
	s := newSettleTimer()
	select {
	case <-s.C():
		t.Fatal("a fresh settle timer fired without being armed")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSettleTimerFiresOnceArmed(t *testing.T) {
	s := newSettleTimer()
	s.arm(10 * time.Millisecond)
	select {
	case <-s.C():
	case <-time.After(time.Second):
		t.Fatal("an armed settle timer never fired")
	}
}

// Re-arming is what coalesces a burst of SIGUSR1 into one check: each request
// pushes the deadline out rather than adding a second check.
func TestSettleTimerReArmingRestartsTheWait(t *testing.T) {
	s := newSettleTimer()
	s.arm(30 * time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	s.arm(30 * time.Millisecond)

	select {
	case <-s.C():
		t.Fatal("re-arming did not restart the wait")
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-s.C():
	case <-time.After(time.Second):
		t.Fatal("the settle timer never fired after re-arming")
	}
}

func TestSettleTimerDisarmCancelsAPendingWait(t *testing.T) {
	s := newSettleTimer()
	s.arm(10 * time.Millisecond)
	s.disarm()

	select {
	case <-s.C():
		t.Fatal("a disarmed settle timer still fired")
	case <-time.After(100 * time.Millisecond):
	}
}

// The case that matters after a remote update run: the path unit watching the
// package database has already armed the timer and it has already fired, but
// nothing has read it yet. Disarming has to take that tick, or the next check
// happens immediately for no reason.
func TestSettleTimerDisarmDrainsATickAlreadyDelivered(t *testing.T) {
	s := newSettleTimer()
	s.arm(10 * time.Millisecond)
	time.Sleep(50 * time.Millisecond) // let it fire, without reading it
	s.disarm()

	select {
	case <-s.C():
		t.Fatal("disarm left a stale tick in the channel")
	case <-time.After(50 * time.Millisecond):
	}

	// And it must still be usable afterwards.
	s.arm(10 * time.Millisecond)
	select {
	case <-s.C():
	case <-time.After(time.Second):
		t.Fatal("the settle timer could not be re-armed after a drained tick")
	}
}
