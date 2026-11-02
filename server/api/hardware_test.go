package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"server/models"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// fakeBenchmarker stands in for the NATS connection in the benchmark tests.
type fakeBenchmarker struct {
	mu    sync.Mutex
	ack   models.BenchmarkAck
	err   error
	hosts []string // every host asked, in order
	by    string
}

func (f *fakeBenchmarker) RequestBenchmark(hostname, requestedBy string) (models.BenchmarkAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts = append(f.hosts, hostname)
	f.by = requestedBy
	return f.ack, f.err
}

func postBenchmark(handler http.HandlerFunc, hostname string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/systems/"+hostname+"/benchmark", nil)
	req = mux.SetURLVars(req, map[string]string{"hostname": hostname})
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestBenchmarkHandler(t *testing.T) {
	supported := models.System{Hostname: "giga", BenchmarkSupported: true}
	tooOld := models.System{Hostname: "mm1"}

	for _, tc := range []struct {
		name    string
		system  models.System
		ack     models.BenchmarkAck
		err     error
		want    int
		wantAsk bool
	}{
		{"accepted", supported, models.BenchmarkAck{Accepted: true, ID: "x"}, nil, http.StatusAccepted, true},
		{"busy host refuses", supported, models.BenchmarkAck{Reason: "busy"}, nil, http.StatusConflict, true},
		{"not listening", supported, models.BenchmarkAck{}, models.ErrHostNotListening, http.StatusServiceUnavailable, true},
		// A client from before benchmarks is never asked: it would only time out.
		{"client too old", tooOld, models.BenchmarkAck{}, nil, http.StatusConflict, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStorage{systems: []models.System{tc.system}}
			f := &fakeBenchmarker{ack: tc.ack, err: tc.err}

			rec := postBenchmark(BenchmarkHandler(store, f), tc.system.Hostname)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body)
			}
			if asked := len(f.hosts) > 0; asked != tc.wantAsk {
				t.Fatalf("host asked = %v, want %v", asked, tc.wantAsk)
			}
			if len(store.ops) != 1 || store.ops[0].Action != "benchmark" {
				t.Fatalf("operations log = %+v, want one benchmark line", store.ops)
			}
		})
	}
}

func TestBenchmarkHandlerWithoutNATS(t *testing.T) {
	store := &fakeStorage{systems: []models.System{{Hostname: "giga", BenchmarkSupported: true}}}
	if rec := postBenchmark(BenchmarkHandler(store, nil), "giga"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHardwareHandler(t *testing.T) {
	bench := &models.Benchmark{SingleThreadMBps: 30, MultiThreadMBps: 240, Threads: 16}
	store := &fakeStorage{systems: []models.System{{
		Hostname:           "rog",
		CPUModel:           "AMD Ryzen 7 3700X 8-Core Processor",
		CPUCores:           16,
		CPUPhysicalCores:   8,
		DiskMedia:          "nvme",
		BenchmarkSupported: true,
		Benchmark:          bench,
		PendingUpdates:     []models.Update{{Name: "kernel"}},
	}}}

	rec := httptest.NewRecorder()
	HardwareHandler(store)(rec, httptest.NewRequest(http.MethodGet, "/api/hardware", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	row := rows[0]
	if row["cpu_physical_cores"] != float64(8) || row["disk_media"] != "nvme" {
		t.Errorf("hardware facts missing: %v", row)
	}
	if b, ok := row["benchmark"].(map[string]any); !ok || b["multi_thread_mbps"] != float64(240) {
		t.Errorf("benchmark missing: %v", row["benchmark"])
	}
	// This is a hardware view; the update list belongs to the dashboard.
	if _, ok := row["pending_updates"]; ok {
		t.Error("hardware rows carry pending updates")
	}
}

func TestSchedulerNextDue(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	seen := now.Add(-time.Minute).Format(time.RFC3339)
	benchedAt := func(ago time.Duration) *models.Benchmark {
		return &models.Benchmark{FinishedAt: now.Add(-ago).Format(time.RFC3339)}
	}

	host := func(name string, b *models.Benchmark) models.System {
		return models.System{Hostname: name, LastSeen: seen, BenchmarkSupported: true, Benchmark: b}
	}

	for _, tc := range []struct {
		name    string
		systems []models.System
		asked   map[string]time.Time
		want    string
	}{
		{"nothing due", []models.System{host("a", benchedAt(time.Hour))}, nil, ""},
		{"never benchmarked comes first",
			[]models.System{host("old", benchedAt(30*24*time.Hour)), host("new", nil)}, nil, "new"},
		{"then the stalest",
			[]models.System{host("b", benchedAt(8*24*time.Hour)), host("a", benchedAt(9*24*time.Hour))}, nil, "a"},
		{"ties go by name", []models.System{host("z", nil), host("m", nil)}, nil, "m"},
		{"old client skipped", []models.System{{Hostname: "mm1", LastSeen: seen}}, nil, ""},
		{"offline host skipped", []models.System{{
			Hostname: "gone", BenchmarkSupported: true,
			LastSeen: now.Add(-time.Hour).Format(time.RFC3339),
		}}, nil, ""},
		{"recently asked skipped",
			[]models.System{host("a", nil), host("b", nil)},
			map[string]time.Time{"a": now.Add(-time.Hour)}, "b"},
		{"asked long ago is asked again",
			[]models.System{host("a", nil)},
			map[string]time.Time{"a": now.Add(-7 * time.Hour)}, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &BenchmarkScheduler{asked: tc.asked}
			if s.asked == nil {
				s.asked = map[string]time.Time{}
			}
			if got := s.nextDue(tc.systems, now); got != tc.want {
				t.Fatalf("nextDue = %q, want %q", got, tc.want)
			}
		})
	}
}

// One tick asks one host, so hosts are never timed alongside each other, and
// the request is logged as the scheduler's.
func TestSchedulerTickAsksOneHost(t *testing.T) {
	now := time.Now()
	seen := now.UTC().Format(time.RFC3339)
	store := &fakeStorage{systems: []models.System{
		{Hostname: "a", LastSeen: seen, BenchmarkSupported: true},
		{Hostname: "b", LastSeen: seen, BenchmarkSupported: true},
	}}
	f := &fakeBenchmarker{ack: models.BenchmarkAck{Accepted: true}}
	s := &BenchmarkScheduler{store: store, requester: f, now: func() time.Time { return now }, asked: map[string]time.Time{}}

	s.Tick()
	s.Tick()
	s.Tick()

	if len(f.hosts) != 2 || f.hosts[0] != "a" || f.hosts[1] != "b" {
		t.Fatalf("asked %v, want [a b] — one per tick, none twice", f.hosts)
	}
	if f.by != SchedulerRequester {
		t.Fatalf("requested by %q, want %q", f.by, SchedulerRequester)
	}
}
