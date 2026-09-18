package nats

import (
	"encoding/json"
	"log/slog"
	"server/metrics"
	"server/models"
	"server/runlog"
	"server/storage"
	"strings"
	"time"

	nats "github.com/nats-io/nats.go"
)

const (
	// checkInSubject is the stream of client check-ins.
	checkInSubject = "systems.updates.>"
	// updateCommandSubjectPrefix carries a request for one host to install its
	// pending packages. It sits outside checkInSubject deliberately.
	updateCommandSubjectPrefix = "systems.commands.update."
	// checkInCommandSubjectPrefix carries a request for one host to check in
	// now. Also outside checkInSubject: it travels the other way.
	checkInCommandSubjectPrefix = "systems.commands.checkin."
	// updateResultSubject is where clients report update-run progress.
	updateResultSubject       = "systems.results.update.>"
	updateResultSubjectPrefix = "systems.results.update."
	// updateOutputSubject is the live output of a run in progress.
	updateOutputSubject       = "systems.output.update.>"
	updateOutputSubjectPrefix = "systems.output.update."
)

// Conn is the server's connection to NATS: it feeds the subscriber and carries
// dashboard-initiated commands back out to the clients.
type Conn struct {
	nc *nats.Conn
}

// Connect opens the server's NATS connection.
func Connect(natsURL string) (*Conn, error) {
	slog.Debug("Attempting to connect to NATS", "url", natsURL)
	nc, err := nats.Connect(natsURL,
		nats.Name("System Updates Subscriber"),
		nats.Timeout(10*time.Second),    // Set a 10-second timeout for the connection
		nats.RetryOnFailedConnect(true), // Retry if initial connection fails
		nats.MaxReconnects(5),           // Attempt to reconnect up to 5 times
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			metrics.NATSConnectionStatus.Set(0)
			slog.Error("Disconnected from NATS", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			metrics.NATSReconnects.Inc()
			metrics.NATSConnectionStatus.Set(1)
			slog.Info("Reconnected to NATS", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			metrics.NATSConnectionStatus.Set(0)
			slog.Info("Connection to NATS closed", "reason", nc.LastError())
		}),
	)
	if err != nil {
		return nil, err
	}

	metrics.NATSConnectionStatus.Set(1) // Set initial connection status
	slog.Info("Successfully connected to NATS", "url", nc.ConnectedUrl())
	slog.Debug("Server ID", "id", nc.ConnectedServerId())

	return &Conn{nc: nc}, nil
}

// Close tears down the connection.
func (c *Conn) Close() {
	c.nc.Close()
}

// StartSubscriber registers the server's subscriptions. It returns once they
// are in place; the handlers run on the connection's own goroutines for as long
// as the connection is open.
func (c *Conn) StartSubscriber(store storage.Storage, runs *runlog.Store) error {
	slog.Debug("Subscribing to subject pattern", "subject", checkInSubject)
	if _, err := c.nc.Subscribe(checkInSubject, checkInHandler(store)); err != nil {
		return err
	}
	slog.Info("Successfully subscribed to subject", "subject", checkInSubject)

	slog.Debug("Subscribing to subject pattern", "subject", updateResultSubject)
	if _, err := c.nc.Subscribe(updateResultSubject, updateResultHandler(store)); err != nil {
		return err
	}
	slog.Info("Successfully subscribed to subject", "subject", updateResultSubject)

	slog.Debug("Subscribing to subject pattern", "subject", updateOutputSubject)
	if _, err := c.nc.Subscribe(updateOutputSubject, updateOutputHandler(runs)); err != nil {
		return err
	}
	slog.Info("Successfully subscribed to subject", "subject", updateOutputSubject)

	slog.Debug("Subscribing to subject pattern", "subject", rebootResultSubject)
	if _, err := c.nc.Subscribe(rebootResultSubject, rebootResultHandler(store)); err != nil {
		return err
	}
	slog.Info("Successfully subscribed to subject", "subject", rebootResultSubject)

	slog.Info("NATS subscriber is now running and listening for messages...")
	return nil
}

func checkInHandler(store storage.Storage) nats.MsgHandler {
	return func(m *nats.Msg) {
		start := time.Now()

		// Record message received
		metrics.NATSMessagesReceived.WithLabelValues(m.Subject).Inc()

		// Update connection status
		metrics.NATSConnectionStatus.Set(1)

		slog.Debug("Received NATS Message", "subject", m.Subject, "reply", m.Reply, "size", len(m.Data))
		slog.Debug("Raw message", "data", string(m.Data))

		// Parse the message into a System struct
		var system models.System
		if err := json.Unmarshal(m.Data, &system); err != nil {
			slog.Error("Failed to unmarshal message", "error", err)
			return
		}

		// Log parsed system data
		slog.Debug("Parsed system data",
			"hostname", system.Hostname,
			"ip", system.Ip,
			"os", system.OS,
			"os_version", system.OSVersion,
			"updates_available", system.UpdatesAvailable,
			"update_status_unknown", system.UpdateStatusUnknown,
			"update_count", len(system.PendingUpdates))

		if system.UpdatesAvailable {
			for _, update := range system.PendingUpdates {
				slog.Debug("Update", "name", update.Name, "version", update.Version, "source", update.Source)
			}
		}

		// Check if this is a first-time check-in
		previous, getErr := store.GetSystem(system.Hostname)
		isFirstTime := getErr != nil

		// Tailnet membership is remembered across check-ins: a host that has
		// left its tailnet — or a client that stopped reporting one — keeps the
		// tailnet it was last seen on, so it shows as disconnected instead of
		// losing its indicator entirely.
		var previousTailscale *models.Tailscale
		if !isFirstTime {
			previousTailscale = previous.Tailscale
			// Update runs arrive on their own subject and are not part of a
			// check-in, so carry the last one forward. Without this the record
			// of a run would survive only until the host next checked in.
			system.LastUpdateRun = previous.LastUpdateRun
			// Likewise the last reboot — and this check-in may be the one that
			// says it happened.
			system.LastReboot = resolveReboot(previous.LastReboot, system.UptimeSeconds, time.Now())
		}
		system.Tailscale = models.MergeTailscale(previousTailscale, system.Tailscale, time.Now())

		// Store the system data
		slog.Debug("Calling SaveSystem", "hostname", system.Hostname)
		startTime := time.Now()
		if err := store.SaveSystem(system.Hostname, system); err != nil {
			slog.Error("Failed to save system data", "hostname", system.Hostname, "error", err, "duration_ms", time.Since(startTime).Milliseconds())
		} else {
			duration := time.Since(startTime)
			if isFirstTime {
				slog.Info("System checked in for the first time", "hostname", system.Hostname, "ip", system.Ip, "os", system.OS, "duration_ms", duration.Milliseconds())
			} else {
				slog.Debug("Successfully saved system data", "hostname", system.Hostname, "duration_ms", duration.Milliseconds())
			}
		}

		// Record processing duration
		duration := time.Since(start).Seconds()
		metrics.NATSMessagesReceivedDuration.WithLabelValues(m.Subject).Observe(duration)
	}
}

// updateResultHandler records the progress of a dashboard-triggered update run
// on the host it belongs to. The hostname comes from the subject rather than
// the payload, so a message cannot claim to be about a different host.
func updateResultHandler(store storage.Storage) nats.MsgHandler {
	return func(m *nats.Msg) {
		metrics.NATSMessagesReceived.WithLabelValues(m.Subject).Inc()

		hostname := strings.TrimPrefix(m.Subject, updateResultSubjectPrefix)
		if hostname == "" || hostname == m.Subject {
			slog.Error("Ignoring update result on an unexpected subject", "subject", m.Subject)
			return
		}

		var run models.UpdateRun
		if err := json.Unmarshal(m.Data, &run); err != nil {
			slog.Error("Failed to unmarshal update run", "hostname", hostname, "error", err)
			return
		}

		system, err := store.GetSystem(hostname)
		if err != nil {
			// A host that has never checked in has nowhere to hang the result.
			slog.Warn("Update result for an unknown system", "hostname", hostname, "error", err)
			return
		}

		system.LastUpdateRun = &run
		if err := store.SaveSystem(hostname, system); err != nil {
			slog.Error("Failed to save update run", "hostname", hostname, "error", err)
			return
		}

		slog.Info("Recorded update run",
			"hostname", hostname, "id", run.ID, "status", run.Status, "exit_code", run.ExitCode)
	}
}

// updateOutputHandler forwards the live output of a run to whoever is watching.
// It never touches storage: this is commentary on a run in flight, and the run
// record that arrives at the end carries the tail worth keeping.
func updateOutputHandler(runs *runlog.Store) nats.MsgHandler {
	return func(m *nats.Msg) {
		hostname := strings.TrimPrefix(m.Subject, updateOutputSubjectPrefix)
		if hostname == "" || hostname == m.Subject {
			slog.Error("Ignoring update output on an unexpected subject", "subject", m.Subject)
			return
		}

		var out struct {
			ID    string `json:"id"`
			Seq   int    `json:"seq"`
			Chunk string `json:"chunk"`
		}
		if err := json.Unmarshal(m.Data, &out); err != nil {
			slog.Error("Failed to unmarshal update output", "hostname", hostname, "error", err)
			return
		}

		runs.Append(hostname, out.ID, out.Seq, out.Chunk)
	}
}
