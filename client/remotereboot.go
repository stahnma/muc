package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stahnma/muc/client/hostinfo"
)

// Remote reboots let the dashboard finish what an update started: a host that
// reports reboot_required can be bounced from the same row. The gating mirrors
// remote updates — the client opts in with allow_remote_reboot, and without it
// never subscribes to the command subject — but the flag is its own, because
// "you may patch this host" and "you may interrupt whatever it is doing" are
// different permissions.
//
// The same caveat as updates applies: NATS is unauthenticated here, so this is
// a convenience for a trusted network, not an authorization boundary. The
// listener does refuse when the host itself sees no reboot pending, which is
// the one check that does not depend on trusting the caller.
const (
	// rebootCommandSubjectPrefix carries the request, addressed to one host.
	rebootCommandSubjectPrefix = "systems.commands.reboot."
	// rebootResultSubjectPrefix carries the record back. Once, as the host goes
	// down: nothing here survives to say it came back, so the server infers
	// that from the next check-in's boot time.
	rebootResultSubjectPrefix = "systems.results.reboot."

	rebootStatusRebooting = "rebooting"
	rebootStatusFailed    = "failed"
)

// rebootRequest is what the server publishes to ask for a reboot.
type rebootRequest struct {
	ID          string `json:"id"`
	RequestedAt string `json:"requested_at"`
	RequestedBy string `json:"requested_by,omitempty"`
}

// rebootAck is the immediate reply: whether this host is going down. It goes
// out before the reboot command runs, so the dashboard hears "yes" from a host
// that is about to stop answering anything.
type rebootAck struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Command  string `json:"command,omitempty"`
}

// rebootRecord is the progress record. Its JSON must stay in step with
// models.Reboot on the server.
type rebootRecord struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	RequestedBy string `json:"requested_by,omitempty"`
	Command     string `json:"command,omitempty"`
	RequestedAt string `json:"requested_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

// rebootListener answers reboot commands for this host.
type rebootListener struct {
	hostname string
	// updateRunning reports whether a dashboard-triggered update is in flight.
	// Rebooting under one would kill a package transaction mid-write.
	updateRunning func() bool
	// rebootRequired is the host's own answer at the moment of the request. The
	// dashboard's button is drawn from the last check-in, which may predate a
	// reboot someone did by hand.
	rebootRequired func() bool
	// reboot runs the reboot command and reports what it ran. A field so the
	// decision can be tested without rebooting the machine running the tests.
	reboot func() (command string, err error)
	// publish sends the record back to the server.
	publish func(rebootRecord)

	mu sync.Mutex
	// inProgress is set once a reboot has been accepted and never cleared on
	// success: the process does not outlive the reboot, and a second request
	// arriving in the seconds before it lands should be told so.
	inProgress bool
}

// startRebootListener subscribes to this host's reboot-command subject.
// updateRunning is consulted at each request; pass a function returning false
// where remote updates are not enabled.
func startRebootListener(nc *nats.Conn, hostname string, updateRunning func() bool) error {
	l := &rebootListener{
		hostname:       hostname,
		updateRunning:  updateRunning,
		rebootRequired: hostinfo.RebootRequired,
		reboot:         runReboot,
		publish:        rebootPublisher(nc, hostname),
	}

	subject := rebootCommandSubjectPrefix + hostname
	if _, err := nc.Subscribe(subject, l.handle); err != nil {
		return fmt.Errorf("subscribing to %s: %w", subject, err)
	}

	slog.Info("Remote reboots enabled; listening for reboot commands", "subject", subject)
	return nil
}

func (l *rebootListener) handle(m *nats.Msg) {
	var req rebootRequest
	if len(m.Data) > 0 {
		if err := json.Unmarshal(m.Data, &req); err != nil {
			slog.Error("Ignoring malformed reboot command", "error", err)
			l.reply(m, rebootAck{Hostname: l.hostname, Reason: "malformed reboot command: " + err.Error()})
			return
		}
	}
	if req.ID == "" {
		req.ID = newRunID()
	}

	ack := l.decide(req)
	l.reply(m, ack)
	if ack.Accepted {
		go l.execute(req)
	}
}

// decide answers one request. Accepting claims the reboot slot; the caller
// then runs execute.
func (l *rebootListener) decide(req rebootRequest) rebootAck {
	ack := rebootAck{ID: req.ID, Hostname: l.hostname}

	l.mu.Lock()
	defer l.mu.Unlock()

	switch {
	case l.inProgress:
		ack.Reason = "a reboot is already in progress on this host"
	case l.updateRunning():
		ack.Reason = "an update is running on this host; reboot it once the run has finished"
	case !l.rebootRequired():
		// The dashboard asked on the strength of an older check-in. Say so,
		// and the check-in the operator asks for next will clear the flag.
		ack.Reason = "this host does not currently need a reboot"
	default:
		l.inProgress = true
		ack.Accepted = true
	}

	if !ack.Accepted {
		slog.Warn("Refusing reboot command", "id", req.ID, "requested_by", req.RequestedBy, "reason", ack.Reason)
	}
	return ack
}

// execute publishes the record and reboots. The record goes first, flushed,
// because nothing after the reboot command can report anything.
func (l *rebootListener) execute(req rebootRequest) {
	requestedAt := req.RequestedAt
	if requestedAt == "" {
		requestedAt = time.Now().UTC().Format(time.RFC3339)
	}
	record := rebootRecord{
		ID:          req.ID,
		Status:      rebootStatusRebooting,
		RequestedBy: req.RequestedBy,
		RequestedAt: requestedAt,
	}
	l.publish(record)

	// The journal is what an operator reads afterwards to learn why the host
	// went down, so say so there in as many words.
	slog.Warn("Rebooting on dashboard request", "id", req.ID, "requested_by", req.RequestedBy)

	command, err := l.reboot()
	record.Command = command
	if err == nil {
		// The command has been accepted by the init system; from here the host
		// goes down and this process with it.
		return
	}

	slog.Error("Reboot command failed", "id", req.ID, "command", command, "error", err)
	record.Status = rebootStatusFailed
	record.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	record.Error = err.Error()
	l.publish(record)

	l.mu.Lock()
	l.inProgress = false
	l.mu.Unlock()
}

// runReboot asks the init system to reboot. It prefers systemctl, which
// schedules the reboot and returns, and falls back to shutdown where there is
// no systemd. It returns the command it ran so the record can say.
func runReboot() (string, error) {
	var argv []string
	if path, err := exec.LookPath("systemctl"); err == nil {
		argv = []string{path, "reboot"}
	} else if path, err := exec.LookPath("shutdown"); err == nil {
		argv = []string{path, "-r", "now"}
	} else {
		return "", errors.New("neither systemctl nor shutdown is available to reboot with")
	}

	command := argv[0] + " " + argv[1]
	if len(argv) > 2 {
		command += " " + argv[2]
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = updateEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := string(out); msg != "" {
			return command, fmt.Errorf("%s: %s", err, msg)
		}
		return command, err
	}
	return command, nil
}

// rebootPublisher sends a record on this host's result subject and flushes:
// the message must have left before the reboot command runs, or it never will.
func rebootPublisher(nc *nats.Conn, hostname string) func(rebootRecord) {
	subject := rebootResultSubjectPrefix + hostname
	return func(record rebootRecord) {
		data, err := json.Marshal(record)
		if err != nil {
			slog.Error("Failed to marshal reboot record", "error", err)
			return
		}
		if err := nc.Publish(subject, data); err != nil {
			slog.Error("Failed to publish reboot record", "subject", subject, "error", err)
			return
		}
		if err := nc.FlushTimeout(5 * time.Second); err != nil {
			slog.Warn("Failed to flush reboot record", "error", err)
		}
	}
}

func (l *rebootListener) reply(m *nats.Msg, ack rebootAck) {
	if m.Reply == "" {
		return
	}
	data, err := json.Marshal(ack)
	if err != nil {
		slog.Error("Failed to marshal reboot ack", "error", err)
		return
	}
	if err := m.Respond(data); err != nil {
		slog.Error("Failed to reply to reboot command", "error", err)
	}
}
