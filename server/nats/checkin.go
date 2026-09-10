package nats

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"server/models"
	"time"

	nats "github.com/nats-io/nats.go"
)

// RequestCheckIn asks one host to collect its state and publish it now, and
// waits for it to accept or refuse. It returns as soon as the host has
// answered: the check-in itself follows a moment later on the ordinary check-in
// subject and reaches the dashboard the way every other one does.
//
// Nothing gates this the way remote updates are gated. A check-in installs
// nothing and reports only what the host publishes on its own every few
// minutes, so every client listens. What the host does enforce is a minimum
// gap between commands, which arrives here as a refusal with a reason.
func (c *Conn) RequestCheckIn(hostname, requestedBy string) (models.CheckInAck, error) {
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
		return models.CheckInAck{}, fmt.Errorf("marshalling check-in request: %w", err)
	}

	subject := checkInCommandSubjectPrefix + hostname
	slog.Info("Requesting check-in", "hostname", hostname, "id", req.ID, "requested_by", requestedBy)

	msg, err := c.nc.Request(subject, data, requestTimeout)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return models.CheckInAck{}, models.ErrHostNotListening
	case errors.Is(err, nats.ErrTimeout):
		return models.CheckInAck{}, fmt.Errorf("%s did not answer within %s", hostname, requestTimeout)
	case err != nil:
		return models.CheckInAck{}, err
	}

	var ack models.CheckInAck
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		return models.CheckInAck{}, fmt.Errorf("unreadable answer from %s: %w", hostname, err)
	}
	ack.Hostname = hostname

	slog.Info("Check-in request answered",
		"hostname", hostname, "id", ack.ID, "accepted", ack.Accepted, "reason", ack.Reason)
	return ack, nil
}
