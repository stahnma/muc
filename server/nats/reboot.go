package nats

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"server/models"
	"server/storage"
	"strings"
	"time"

	nats "github.com/nats-io/nats.go"
)

const (
	// rebootCommandSubjectPrefix carries a request for one host to reboot.
	rebootCommandSubjectPrefix = "systems.commands.reboot."
	// rebootResultSubject is where clients report a reboot as they go down.
	rebootResultSubject       = "systems.results.reboot.>"
	rebootResultSubjectPrefix = "systems.results.reboot."
)

// RequestReboot asks one host to reboot and waits for it to accept or refuse.
// It returns as soon as the host has answered. The host publishes its record on
// the result subject a moment later and then goes down; the reboot is marked
// complete by the server itself, from the boot time in the host's next check-in
// (see checkInHandler).
func (c *Conn) RequestReboot(hostname, requestedBy string) (models.RebootAck, error) {
	req := struct {
		ID          string `json:"id"`
		RequestedAt string `json:"requested_at"`
		RequestedBy string `json:"requested_by,omitempty"`
	}{
		ID:          newRequestID(),
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
		RequestedBy: requestedBy,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return models.RebootAck{}, fmt.Errorf("marshalling reboot request: %w", err)
	}

	subject := rebootCommandSubjectPrefix + hostname
	slog.Warn("Requesting reboot", "hostname", hostname, "id", req.ID, "requested_by", requestedBy)

	msg, err := c.nc.Request(subject, data, requestTimeout)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return models.RebootAck{}, models.ErrHostNotListening
	case errors.Is(err, nats.ErrTimeout):
		return models.RebootAck{}, fmt.Errorf("%s did not answer within %s", hostname, requestTimeout)
	case err != nil:
		return models.RebootAck{}, err
	}

	var ack models.RebootAck
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		return models.RebootAck{}, fmt.Errorf("unreadable answer from %s: %w", hostname, err)
	}
	ack.Hostname = hostname

	slog.Info("Reboot request answered",
		"hostname", hostname, "id", ack.ID, "accepted", ack.Accepted, "reason", ack.Reason)
	return ack, nil
}

// rebootResultHandler records a dashboard-triggered reboot on the host it
// belongs to. As with update results the hostname comes from the subject, so a
// message cannot claim to be about a different host.
func rebootResultHandler(store storage.Storage) nats.MsgHandler {
	return func(m *nats.Msg) {
		hostname := strings.TrimPrefix(m.Subject, rebootResultSubjectPrefix)
		if hostname == "" || hostname == m.Subject {
			slog.Error("Ignoring reboot result on an unexpected subject", "subject", m.Subject)
			return
		}

		var reboot models.Reboot
		if err := json.Unmarshal(m.Data, &reboot); err != nil {
			slog.Error("Failed to unmarshal reboot record", "hostname", hostname, "error", err)
			return
		}
		if reboot.RequestedAt == "" {
			reboot.RequestedAt = time.Now().UTC().Format(time.RFC3339)
		}

		system, err := store.GetSystem(hostname)
		if err != nil {
			slog.Warn("Reboot result for an unknown system", "hostname", hostname, "error", err)
			return
		}

		system.LastReboot = &reboot
		if err := store.SaveSystem(hostname, system); err != nil {
			slog.Error("Failed to save reboot record", "hostname", hostname, "error", err)
			return
		}

		slog.Info("Recorded reboot", "hostname", hostname, "id", reboot.ID, "status", reboot.Status)
	}
}

// resolveReboot carries a host's reboot record across a check-in and, where the
// check-in shows the host has been up only since after the request, marks the
// reboot done. The host cannot report this itself: the client that would have
// did not survive the reboot.
//
// Boot time is derived from the host's own uptime and the server's receipt
// time, and compared with the request time the server stamped, so the two clocks
// involved are the same one. A check-in with no uptime at all cannot answer
// the question and leaves the record as it was.
func resolveReboot(previous *models.Reboot, uptimeSeconds uint64, now time.Time) *models.Reboot {
	if previous == nil || previous.Status != models.RebootStatusRebooting || uptimeSeconds == 0 {
		return previous
	}
	requestedAt, err := time.Parse(time.RFC3339, previous.RequestedAt)
	if err != nil {
		return previous
	}
	bootedAt := now.Add(-time.Duration(uptimeSeconds) * time.Second)
	if !bootedAt.After(requestedAt) {
		return previous
	}

	done := *previous
	done.Status = models.RebootStatusRebooted
	done.FinishedAt = now.UTC().Format(time.RFC3339)
	return &done
}
