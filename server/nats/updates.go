package nats

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"server/models"
	"time"

	nats "github.com/nats-io/nats.go"
)

// requestTimeout bounds the wait for a host to say whether it took the job. The
// answer is an acknowledgement, not the run itself, so a host that is up
// answers in milliseconds; this only has to cover a slow link.
const requestTimeout = 10 * time.Second

// RequestUpdate asks one host to install its pending packages and waits for it
// to accept or refuse. It returns as soon as the host has answered — the run
// itself reports back separately, on the result subject.
func (c *Conn) RequestUpdate(hostname, requestedBy string) (models.UpdateAck, error) {
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
		return models.UpdateAck{}, fmt.Errorf("marshalling update request: %w", err)
	}

	subject := updateCommandSubjectPrefix + hostname
	slog.Info("Requesting update run", "hostname", hostname, "id", req.ID, "requested_by", requestedBy)

	msg, err := c.nc.Request(subject, data, requestTimeout)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return models.UpdateAck{}, models.ErrHostNotListening
	case errors.Is(err, nats.ErrTimeout):
		return models.UpdateAck{}, fmt.Errorf("%s did not answer within %s", hostname, requestTimeout)
	case err != nil:
		return models.UpdateAck{}, err
	}

	var ack models.UpdateAck
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		return models.UpdateAck{}, fmt.Errorf("unreadable answer from %s: %w", hostname, err)
	}
	ack.Hostname = hostname

	slog.Info("Update request answered",
		"hostname", hostname, "id", ack.ID, "accepted", ack.Accepted, "reason", ack.Reason)
	return ack, nil
}

// newRequestID labels a run so the dashboard can tell one from the next.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
