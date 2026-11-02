package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/stahnma/muc/client/benchmark"
)

// A benchmark command asks this host to time a fixed CPU workload and publish
// the result, so the dashboard's hardware page can compare hosts by what they
// actually do. The server asks once a host first shows up and again weekly;
// the page has a button to ask sooner.
//
// Like check-ins it needs no opt-in: it reads nothing and changes nothing. What
// it does cost is about six seconds of CPU, half of it on every core, so the
// listener bounds that the way the check-in listener bounds metadata refreshes
// — and also declines to run on a host that is already busy, because the
// number would be wrong and the host's real work would pay for it.
const (
	benchmarkCommandSubjectPrefix = "systems.commands.benchmark."
	benchmarkResultSubjectPrefix  = "systems.results.benchmark."

	// minBenchmarkInterval is the shortest gap between two accepted runs.
	minBenchmarkInterval = 10 * time.Minute

	// busyLoadPerCPU is the one-minute load average, per CPU, above which the
	// host is too busy to be measured fairly.
	busyLoadPerCPU = 0.5
)

type benchmarkRequest struct {
	ID          string `json:"id"`
	RequestedAt string `json:"requested_at"`
	RequestedBy string `json:"requested_by,omitempty"`
}

// benchmarkAck is the immediate reply. As with check-ins it comes before the
// work: the run outlasts what the server will wait for an answer.
type benchmarkAck struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// benchmarkRecord is what is published when a run ends. Its JSON must stay in
// step with the server's models.Benchmark.
type benchmarkRecord struct {
	ID          string `json:"id"`
	RequestedBy string `json:"requested_by,omitempty"`
	FinishedAt  string `json:"finished_at"`
	benchmark.Result
}

type benchmarkListener struct {
	nc       *nats.Conn
	hostname string

	// The seams below exist for tests: the clock, the load reading and the
	// workload itself.
	now         func() time.Time
	loadPerCPU  func() (float64, error)
	run         func() benchmark.Result
	minInterval time.Duration

	mu      sync.Mutex
	last    time.Time
	running bool
}

func startBenchmarkListener(nc *nats.Conn, hostname string) error {
	l := &benchmarkListener{
		nc:          nc,
		hostname:    hostname,
		now:         time.Now,
		loadPerCPU:  loadPerCPU,
		run:         benchmark.Run,
		minInterval: minBenchmarkInterval,
	}

	subject := benchmarkCommandSubjectPrefix + hostname
	if _, err := nc.Subscribe(subject, l.handle); err != nil {
		return fmt.Errorf("subscribing to %s: %w", subject, err)
	}
	slog.Info("Listening for benchmark commands", "subject", subject,
		"min_interval", minBenchmarkInterval)
	return nil
}

func (l *benchmarkListener) handle(m *nats.Msg) {
	var req benchmarkRequest
	if len(m.Data) > 0 {
		if err := json.Unmarshal(m.Data, &req); err != nil {
			slog.Error("Ignoring malformed benchmark command", "error", err)
			l.reply(m, benchmarkAck{Hostname: l.hostname, Reason: "malformed benchmark command: " + err.Error()})
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

// decide answers one request. Accepting claims the run, so a second request
// landing while the first is still timing is refused rather than queued.
func (l *benchmarkListener) decide(req benchmarkRequest) benchmarkAck {
	ack := benchmarkAck{ID: req.ID, Hostname: l.hostname}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.running {
		ack.Reason = "a benchmark is already running on this host"
		return ack
	}
	now := l.now()
	if wait := l.minInterval - now.Sub(l.last); !l.last.IsZero() && wait > 0 {
		ack.Reason = fmt.Sprintf("this host ran a benchmark less than %s ago; try again in %s",
			l.minInterval, ceilSeconds(wait))
		return ack
	}
	if busy, err := l.loadPerCPU(); err == nil && busy > busyLoadPerCPU {
		ack.Reason = fmt.Sprintf("this host is busy (load %.2f per CPU); a benchmark now would measure the load, not the hardware", busy)
		return ack
	}

	l.last = now
	l.running = true
	slog.Info("Accepted benchmark command", "id", req.ID, "requested_by", req.RequestedBy)
	ack.Accepted = true
	return ack
}

func (l *benchmarkListener) execute(req benchmarkRequest) {
	defer func() {
		l.mu.Lock()
		l.running = false
		l.mu.Unlock()
	}()

	res := l.run()
	rec := benchmarkRecord{
		ID:          req.ID,
		RequestedBy: req.RequestedBy,
		FinishedAt:  l.now().UTC().Format(time.RFC3339),
		Result:      res,
	}
	slog.Info("Benchmark finished", "id", req.ID,
		"single_thread_mbps", fmt.Sprintf("%.1f", res.SingleThreadMBps),
		"multi_thread_mbps", fmt.Sprintf("%.1f", res.MultiThreadMBps),
		"threads", res.Threads)

	data, err := json.Marshal(rec)
	if err != nil {
		slog.Error("Failed to marshal benchmark result", "error", err)
		return
	}
	if err := l.nc.Publish(benchmarkResultSubjectPrefix+l.hostname, data); err != nil {
		slog.Error("Failed to publish benchmark result", "error", err)
		return
	}
	if err := l.nc.Flush(); err != nil {
		slog.Error("Failed to flush benchmark result", "error", err)
	}
}

func (l *benchmarkListener) reply(m *nats.Msg, ack benchmarkAck) {
	if m.Reply == "" {
		return
	}
	data, err := json.Marshal(ack)
	if err != nil {
		slog.Error("Failed to marshal benchmark ack", "error", err)
		return
	}
	if err := m.Respond(data); err != nil {
		slog.Error("Failed to reply to benchmark command", "error", err)
	}
}

// loadPerCPU is the one-minute load average divided by the CPUs the
// benchmark would use.
func loadPerCPU() (float64, error) {
	avg, err := load.Avg()
	if err != nil {
		return 0, err
	}
	return avg.Load1 / float64(runtime.GOMAXPROCS(0)), nil
}
