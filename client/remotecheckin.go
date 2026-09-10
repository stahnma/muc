package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// A check-in command asks this host to read its package manager now and publish
// what it finds, rather than waiting out the poll interval. It is the button
// beside "Run updates now" on the dashboard.
//
// Unlike remote updates this needs no opt-in at either end, so the listener
// starts on every client. Nothing it publishes is anything the client does not
// already publish every few minutes of its own accord, and it installs nothing:
// there is no state for a caller to change, only work for it to ask for.
//
// The work is the part worth bounding. A check is a package-manager query, and
// `dnf check-update --refresh` talks to every configured repository, so an
// unbounded command subject would let anything that can reach NATS keep a host
// refreshing metadata continuously. minCheckInInterval caps that; see decide.
const (
	// checkInCommandSubjectPrefix carries the request, addressed to one host.
	// Like the update command subject it sits outside "systems.updates.>",
	// which is the check-in stream travelling the other way.
	checkInCommandSubjectPrefix = "systems.commands.checkin."

	// minCheckInInterval is the shortest gap between two commanded check-ins.
	// It is short enough that an operator who reads the result, doubts it and
	// asks again is not told to wait, and long enough that the subject cannot
	// be used to hold a host at a full-time metadata refresh.
	minCheckInInterval = 10 * time.Second
)

// checkInRequest is what the server publishes to ask for a check-in.
type checkInRequest struct {
	ID          string `json:"id"`
	RequestedAt string `json:"requested_at"`
	RequestedBy string `json:"requested_by,omitempty"`
}

// checkInAck is the immediate reply: whether this host will check in. It says
// nothing about the result, because the result is the check-in itself, which
// travels the ordinary subject and reaches the dashboard as any other one does.
//
// Answering before the check runs is deliberate. A cold `dnf check-update
// --refresh` can take longer than the server is willing to wait for a reply, so
// a host that answered only when it had an answer would look unreachable
// precisely when it had the most work to do.
type checkInAck struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

type checkInListener struct {
	hostname string
	// recheck asks the main loop to collect and publish now. It is the same
	// channel a finished update run uses, and the loop treats both alike.
	recheck chan<- struct{}
	// now and minInterval are fields so the guard below can be tested without
	// waiting it out.
	now         func() time.Time
	minInterval time.Duration

	mu sync.Mutex
	// last is when a command was last accepted, not when the check it asked
	// for finished: the point is to rate-limit the asking.
	last time.Time
}

// startCheckInListener subscribes to this host's check-in command subject.
func startCheckInListener(nc *nats.Conn, hostname string, recheck chan<- struct{}) error {
	l := &checkInListener{
		hostname:    hostname,
		recheck:     recheck,
		now:         time.Now,
		minInterval: minCheckInInterval,
	}

	subject := checkInCommandSubjectPrefix + hostname
	if _, err := nc.Subscribe(subject, l.handle); err != nil {
		return fmt.Errorf("subscribing to %s: %w", subject, err)
	}

	slog.Info("Listening for check-in commands", "subject", subject,
		"min_interval", minCheckInInterval)
	return nil
}

func (l *checkInListener) handle(m *nats.Msg) {
	var req checkInRequest
	if len(m.Data) > 0 {
		if err := json.Unmarshal(m.Data, &req); err != nil {
			slog.Error("Ignoring malformed check-in command", "error", err)
			l.reply(m, checkInAck{Hostname: l.hostname, Reason: "malformed check-in command: " + err.Error()})
			return
		}
	}
	if req.ID == "" {
		req.ID = newRunID()
	}

	l.reply(m, l.decide(req))
}

// decide answers one request and, when it accepts, asks the main loop for the
// check. Accepting is the trigger, so the two are one step: there is nothing
// the caller could usefully do between them.
func (l *checkInListener) decide(req checkInRequest) checkInAck {
	ack := checkInAck{ID: req.ID, Hostname: l.hostname}

	l.mu.Lock()
	now := l.now()
	wait := l.minInterval - now.Sub(l.last)
	if !l.last.IsZero() && wait > 0 {
		l.mu.Unlock()
		slog.Debug("Refusing check-in command; one was accepted moments ago",
			"id", req.ID, "requested_by", req.RequestedBy, "retry_in", wait)
		ack.Reason = fmt.Sprintf("this host checked in less than %s ago; try again in %s",
			l.minInterval, ceilSeconds(wait))
		return ack
	}
	l.last = now
	l.mu.Unlock()

	slog.Info("Accepted check-in command", "id", req.ID, "requested_by", req.RequestedBy)
	ack.Accepted = true

	// Non-blocking, like the update runner's request: the channel holds one
	// check, and a second landing while the first is still queued is asking for
	// exactly the check that is about to happen.
	select {
	case l.recheck <- struct{}{}:
	default:
	}
	return ack
}

func (l *checkInListener) reply(m *nats.Msg, ack checkInAck) {
	if m.Reply == "" {
		return
	}
	data, err := json.Marshal(ack)
	if err != nil {
		slog.Error("Failed to marshal check-in ack", "error", err)
		return
	}
	if err := m.Respond(data); err != nil {
		slog.Error("Failed to reply to check-in command", "error", err)
	}
}

// ceilSeconds rounds a wait up to the next whole second, so a refusal never
// tells an operator to try again in "0s".
func ceilSeconds(d time.Duration) time.Duration {
	if truncated := d.Truncate(time.Second); truncated < d {
		return truncated + time.Second
	}
	return d
}
