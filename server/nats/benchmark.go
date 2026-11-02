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
	// benchmarkCommandSubjectPrefix carries a request for one host to run its
	// CPU benchmark.
	benchmarkCommandSubjectPrefix = "systems.commands.benchmark."
	// benchmarkResultSubject is where clients report a finished benchmark.
	benchmarkResultSubject       = "systems.results.benchmark.>"
	benchmarkResultSubjectPrefix = "systems.results.benchmark."
)

// RequestBenchmark asks one host to run its benchmark and waits for it to
// accept or refuse. The result follows on the result subject once the run is
// done, a few seconds later.
func (c *Conn) RequestBenchmark(hostname, requestedBy string) (models.BenchmarkAck, error) {
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
		return models.BenchmarkAck{}, fmt.Errorf("marshalling benchmark request: %w", err)
	}

	subject := benchmarkCommandSubjectPrefix + hostname
	slog.Info("Requesting benchmark", "hostname", hostname, "id", req.ID, "requested_by", requestedBy)

	msg, err := c.nc.Request(subject, data, requestTimeout)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return models.BenchmarkAck{}, models.ErrHostNotListening
	case errors.Is(err, nats.ErrTimeout):
		return models.BenchmarkAck{}, fmt.Errorf("%s did not answer within %s", hostname, requestTimeout)
	case err != nil:
		return models.BenchmarkAck{}, err
	}

	var ack models.BenchmarkAck
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		return models.BenchmarkAck{}, fmt.Errorf("unreadable answer from %s: %w", hostname, err)
	}
	ack.Hostname = hostname

	slog.Info("Benchmark request answered",
		"hostname", hostname, "id", ack.ID, "accepted", ack.Accepted, "reason", ack.Reason)
	return ack, nil
}

// benchmarkResultHandler records a finished benchmark on the host it belongs
// to. As with the other results the hostname comes from the subject, so a
// message cannot claim to be about a different host.
func benchmarkResultHandler(store storage.Storage) nats.MsgHandler {
	return func(m *nats.Msg) {
		hostname := strings.TrimPrefix(m.Subject, benchmarkResultSubjectPrefix)
		if hostname == "" || hostname == m.Subject {
			slog.Error("Ignoring benchmark result on an unexpected subject", "subject", m.Subject)
			return
		}

		var bench models.Benchmark
		if err := json.Unmarshal(m.Data, &bench); err != nil {
			slog.Error("Failed to unmarshal benchmark result", "hostname", hostname, "error", err)
			return
		}
		if bench.FinishedAt == "" {
			bench.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		}

		system, err := store.GetSystem(hostname)
		if err != nil {
			slog.Warn("Benchmark result for an unknown system", "hostname", hostname, "error", err)
			return
		}

		system.Benchmark = &bench
		if err := store.SaveSystem(hostname, system); err != nil {
			slog.Error("Failed to save benchmark result", "hostname", hostname, "error", err)
			return
		}

		slog.Info("Recorded benchmark", "hostname", hostname, "id", bench.ID,
			"single_thread_mbps", bench.SingleThreadMBps, "multi_thread_mbps", bench.MultiThreadMBps)

		logOperation(store, models.Operation{
			Hostname:    hostname,
			Action:      "benchmark",
			Event:       models.OperationSucceeded,
			ID:          bench.ID,
			RequestedBy: bench.RequestedBy,
			Detail: fmt.Sprintf("%.1f MB/s on one thread, %.1f MB/s on %d",
				bench.SingleThreadMBps, bench.MultiThreadMBps, bench.Threads),
		})
	}
}
