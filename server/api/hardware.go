package api

import (
	"log/slog"
	"net/http"
	"server/models"
	"server/storage"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

// BenchmarkRequester asks one host to run its CPU benchmark, returning the
// host's own answer. Implemented by the NATS connection; nil only when there is
// no connection to ask over, since benchmarks are not gated by configuration.
type BenchmarkRequester interface {
	RequestBenchmark(hostname, requestedBy string) (models.BenchmarkAck, error)
}

// HardwareSummary is one row of the hardware page: what a host is made of and
// what its benchmark measured. It is its own response rather than more fields
// on SystemSummary because the dashboard polls that one, and has no use for
// any of this.
type HardwareSummary struct {
	Hostname             string            `json:"hostname"`
	Architecture         string            `json:"architecture"`
	OS                   string            `json:"os"`
	LastSeen             string            `json:"last_seen"`
	CPUModel             string            `json:"cpu_model"`
	CPUCores             int               `json:"cpu_cores"`
	CPUPhysicalCores     int               `json:"cpu_physical_cores,omitempty"`
	CPUMaxMHz            float64           `json:"cpu_max_mhz,omitempty"`
	Virtualization       string            `json:"virtualization,omitempty"`
	MemoryTotalBytes     uint64            `json:"memory_total_bytes"`
	MemoryAvailableBytes uint64            `json:"memory_available_bytes,omitempty"`
	DiskTotalBytes       uint64            `json:"disk_total_bytes,omitempty"`
	DiskFreeBytes        uint64            `json:"disk_free_bytes,omitempty"`
	DiskMedia            string            `json:"disk_media,omitempty"`
	BenchmarkSupported   bool              `json:"benchmark_supported"`
	Benchmark            *models.Benchmark `json:"benchmark,omitempty"`
}

// HardwareHandler lists every host's hardware and latest benchmark.
func HardwareHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		systems, err := store.GetAllSystems()
		if err != nil {
			slog.Error("Failed to get all systems", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "Failed to fetch systems")
			return
		}

		rows := make([]HardwareSummary, 0, len(systems))
		for _, s := range systems {
			rows = append(rows, HardwareSummary{
				Hostname:             s.Hostname,
				Architecture:         s.Architecture,
				OS:                   s.OS,
				LastSeen:             s.LastSeen,
				CPUModel:             s.CPUModel,
				CPUCores:             s.CPUCores,
				CPUPhysicalCores:     s.CPUPhysicalCores,
				CPUMaxMHz:            s.CPUMaxMHz,
				Virtualization:       s.Virtualization,
				MemoryTotalBytes:     s.MemoryTotalBytes,
				MemoryAvailableBytes: s.MemoryAvailableBytes,
				DiskTotalBytes:       s.DiskTotalBytes,
				DiskFreeBytes:        s.DiskFreeBytes,
				DiskMedia:            s.DiskMedia,
				BenchmarkSupported:   s.BenchmarkSupported,
				Benchmark:            s.Benchmark,
			})
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

// BenchmarkHandler asks a host to run its benchmark now rather than waiting
// for the scheduler.
func BenchmarkHandler(store storage.Storage, requester BenchmarkRequester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requester == nil {
			writeAPIError(w, http.StatusServiceUnavailable,
				"Benchmark requests are unavailable: the server has no NATS connection")
			return
		}

		hostname := strings.TrimSpace(mux.Vars(r)["hostname"])
		if hostname == "" {
			writeAPIError(w, http.StatusBadRequest, "Hostname is required")
			return
		}

		res := dispatch(store, actionBenchmark, requesters{benchmark: requester}, hostname, requesterAddress(r), "")
		if res.Outcome != OutcomeAccepted {
			writeAPIError(w, statusFor(res), res.Reason)
			return
		}

		writeJSON(w, http.StatusAccepted, map[string]string{
			"status":   "accepted",
			"hostname": hostname,
			"id":       res.ID,
			"message":  hostname + " is running its benchmark",
		})
	}
}

const (
	// BenchmarkMaxAge is how old a benchmark gets before the scheduler asks
	// for a fresh one. Hardware does not change weekly, but a VM can be
	// resized, a host can move, and a kernel or firmware update can change a
	// score; a week keeps the page honest without spending CPU on it.
	BenchmarkMaxAge = 7 * 24 * time.Hour

	// benchmarkTick is how often the scheduler looks for a host to ask. It
	// asks at most one per tick, so no two hosts are ever timed at the same
	// moment — which matters when one is a VM on another — and a whole new
	// fleet is covered in a tick per host.
	benchmarkTick = 2 * time.Minute

	// benchmarkRetryAfter is how long the scheduler leaves a host it asked
	// before asking it again, whatever the answer was. A busy host is not
	// pestered, and a refusal or a lost result costs one log line per period
	// rather than one per tick.
	benchmarkRetryAfter = 6 * time.Hour

	// benchmarkSeenWithin is how recently a host must have checked in to be
	// asked. A host that is down would only add a "host unreachable" line.
	benchmarkSeenWithin = 15 * time.Minute

	// SchedulerRequester is what the operations log shows as having asked
	// for a scheduled benchmark.
	SchedulerRequester = "scheduler"
)

// BenchmarkScheduler asks hosts for a benchmark when they have none, or their
// last is older than BenchmarkMaxAge: a new host is benchmarked within a few
// minutes of its first check-in, and every host weekly after that.
type BenchmarkScheduler struct {
	store     storage.Storage
	requester BenchmarkRequester
	now       func() time.Time

	mu sync.Mutex
	// asked is when each host was last asked, in memory only. Losing it on a
	// restart costs one early retry per host, which is not worth storing.
	asked map[string]time.Time
}

// StartBenchmarkScheduler runs the scheduler for the life of the process.
func StartBenchmarkScheduler(store storage.Storage, requester BenchmarkRequester) *BenchmarkScheduler {
	s := &BenchmarkScheduler{
		store:     store,
		requester: requester,
		now:       time.Now,
		asked:     make(map[string]time.Time),
	}
	go func() {
		ticker := time.NewTicker(benchmarkTick)
		defer ticker.Stop()
		for range ticker.C {
			s.Tick()
		}
	}()
	slog.Info("Benchmark scheduler started", "max_age", BenchmarkMaxAge, "tick", benchmarkTick)
	return s
}

// Tick asks the most overdue host, if any is due, for a benchmark.
func (s *BenchmarkScheduler) Tick() {
	systems, err := s.store.GetAllSystems()
	if err != nil {
		slog.Error("Benchmark scheduler could not list systems", "error", err)
		return
	}

	s.mu.Lock()
	host := s.nextDue(systems, s.now())
	if host != "" {
		s.asked[host] = s.now()
	}
	s.mu.Unlock()

	if host == "" {
		return
	}
	res := dispatch(s.store, actionBenchmark, requesters{benchmark: s.requester}, host, SchedulerRequester, "")
	slog.Debug("Scheduled benchmark requested", "hostname", host, "outcome", res.Outcome, "reason", res.Reason)
}

// nextDue picks the host to ask: one that can be asked, is up, has not been
// asked lately, and whose benchmark is missing or stale. Hosts with none come
// first, then the oldest; ties go by name so the order is stable.
func (s *BenchmarkScheduler) nextDue(systems []models.System, now time.Time) string {
	type candidate struct {
		host string
		last time.Time // zero when the host has never been benchmarked
	}
	var due []candidate
	for _, sys := range systems {
		if !sys.BenchmarkSupported {
			continue
		}
		if seen, err := time.Parse(time.RFC3339, sys.LastSeen); err != nil || now.Sub(seen) > benchmarkSeenWithin {
			continue
		}
		if asked, ok := s.asked[sys.Hostname]; ok && now.Sub(asked) < benchmarkRetryAfter {
			continue
		}
		var last time.Time
		if sys.Benchmark != nil {
			last, _ = time.Parse(time.RFC3339, sys.Benchmark.FinishedAt)
			if !last.IsZero() && now.Sub(last) < BenchmarkMaxAge {
				continue
			}
		}
		due = append(due, candidate{sys.Hostname, last})
	}
	if len(due) == 0 {
		return ""
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].last.Equal(due[j].last) {
			return due[i].last.Before(due[j].last)
		}
		return due[i].host < due[j].host
	})
	return due[0].host
}
