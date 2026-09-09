package main

import "time"

// settleTimer coalesces deferred re-check requests into one.
//
// A single package transaction writes the rpm/dpkg database many times, and the
// last write lands before rpm has finished running its scriptlets, so the path
// unit watching those files fires SIGUSR1 several times for what is one event.
// Arming restarts the wait, which turns that burst into a single check once the
// writes stop.
//
// It is a type mainly for the sake of the drain: a Go timer that has already
// fired leaves a value in its channel, and re-arming without taking that value
// would deliver a stale tick straight away.
type settleTimer struct {
	timer *time.Timer
}

func newSettleTimer() *settleTimer {
	s := &settleTimer{timer: time.NewTimer(0)}
	s.disarm()
	return s
}

// C fires once a wait has elapsed with nothing re-arming it.
func (s *settleTimer) C() <-chan time.Time { return s.timer.C }

// arm restarts the wait, discarding any wait already in progress.
func (s *settleTimer) arm(d time.Duration) {
	s.disarm()
	s.timer.Reset(d)
}

// disarm cancels a pending wait, whether or not it has already fired. Use it
// when a check has just happened for other reasons and the pending one would
// only repeat it.
func (s *settleTimer) disarm() {
	if !s.timer.Stop() {
		// Drain only a tick that was actually delivered; a stopped-but-unfired
		// timer has nothing in its channel, and this would block forever
		// without the default.
		select {
		case <-s.timer.C:
		default:
		}
	}
}
