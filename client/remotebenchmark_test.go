package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stahnma/muc/client/benchmark"
)

func newTestBenchmarkListener(now *time.Time, load float64) *benchmarkListener {
	return &benchmarkListener{
		hostname:    "testhost",
		now:         func() time.Time { return *now },
		loadPerCPU:  func() (float64, error) { return load, nil },
		run:         func() benchmark.Result { return benchmark.Result{} },
		minInterval: minBenchmarkInterval,
	}
}

func TestBenchmarkDecideRateLimits(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	l := newTestBenchmarkListener(&now, 0)

	if ack := l.decide(benchmarkRequest{ID: "a"}); !ack.Accepted {
		t.Fatalf("first request refused: %s", ack.Reason)
	}
	l.running = false // as execute would leave it

	now = now.Add(time.Minute)
	ack := l.decide(benchmarkRequest{ID: "b"})
	if ack.Accepted {
		t.Fatal("second request within the interval was accepted")
	}
	if !strings.Contains(ack.Reason, "try again in 9m0s") {
		t.Fatalf("reason = %q, want the remaining wait", ack.Reason)
	}

	now = now.Add(minBenchmarkInterval)
	if ack := l.decide(benchmarkRequest{ID: "c"}); !ack.Accepted {
		t.Fatalf("request after the interval refused: %s", ack.Reason)
	}
}

func TestBenchmarkDecideRefusesWhileRunning(t *testing.T) {
	now := time.Now()
	l := newTestBenchmarkListener(&now, 0)
	l.minInterval = 0
	l.decide(benchmarkRequest{ID: "a"})

	ack := l.decide(benchmarkRequest{ID: "b"})
	if ack.Accepted || !strings.Contains(ack.Reason, "already running") {
		t.Fatalf("ack = %+v, want refused as already running", ack)
	}
}

func TestBenchmarkDecideRefusesBusyHost(t *testing.T) {
	now := time.Now()
	l := newTestBenchmarkListener(&now, 0.9)

	ack := l.decide(benchmarkRequest{ID: "a"})
	if ack.Accepted || !strings.Contains(ack.Reason, "busy") {
		t.Fatalf("ack = %+v, want refused as busy", ack)
	}
	// A refusal must not use up the interval, or a busy moment would block
	// the next attempt for ten minutes.
	if !l.last.IsZero() || l.running {
		t.Fatal("a refused request claimed the run")
	}
}

// Not being able to read the load is no reason to refuse: the run is still
// worth more than no run.
func TestBenchmarkDecideIgnoresUnreadableLoad(t *testing.T) {
	now := time.Now()
	l := newTestBenchmarkListener(&now, 0)
	l.loadPerCPU = func() (float64, error) { return 0, errors.New("no /proc/loadavg") }

	if ack := l.decide(benchmarkRequest{ID: "a"}); !ack.Accepted {
		t.Fatalf("refused: %s", ack.Reason)
	}
}
