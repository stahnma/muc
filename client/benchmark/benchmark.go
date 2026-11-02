// Package benchmark measures how much CPU this host can actually bring to bear,
// so hosts can be compared by what they do rather than by the name of the chip
// they run on. A VM with one vCPU of a 64-core EPYC reports that EPYC's model
// string; only running something says it is slower than a laptop.
//
// The workload is DEFLATE compression of a fixed, generated corpus, using the
// standard library's compress/flate. It is pure Go — no assembly, no crypto or
// SIMD instructions that one architecture has and another lacks — so an x86
// host and an ARM board are timed doing the same work the same way. It is a mix
// of hashing, branching and memory traffic, which is closer to "general
// compute" than a tight arithmetic loop, and its result reads as a unit people
// already have a feel for: megabytes compressed per second.
package benchmark

import (
	"bytes"
	"compress/flate"
	"runtime"
	"sync"
	"time"
)

// Version identifies the workload. Scores from different versions are not
// comparable, so it travels with every result and the dashboard can say so.
const Version = 1

const (
	// corpusSize is how much is compressed per operation. Large enough that
	// one operation is a few milliseconds on a slow board, small enough that
	// the per-worker corpus and compressor stay well inside a 1 GiB VM.
	corpusSize = 256 << 10

	// roundDuration is how long each round runs, and rounds how many are
	// run per mode. The best round is kept: noise from a neighbour or a cron
	// job only ever makes a round slower, so the fastest is the closest to what
	// the hardware can do.
	roundDuration = time.Second
	rounds        = 3
)

// Result is one benchmark run. Its JSON must stay in step with the server's
// models.Benchmark.
type Result struct {
	Version int `json:"version"`
	// SingleThreadMBps is compression throughput on one goroutine.
	SingleThreadMBps float64 `json:"single_thread_mbps"`
	// MultiThreadMBps is the aggregate throughput of one goroutine per CPU
	// the Go runtime will use — which honours a container's CPU quota, so it
	// is what this host can actually spend rather than what /proc/cpuinfo
	// lists.
	MultiThreadMBps float64 `json:"multi_thread_mbps"`
	Threads         int     `json:"threads"`
	// DurationSeconds is the wall time of the whole run.
	DurationSeconds float64 `json:"duration_seconds"`
}

// Run measures single- then multi-threaded throughput. It takes about
// 2 × rounds × roundDuration and keeps every CPU busy for half of that.
func Run() Result {
	start := time.Now()
	corpus := makeCorpus(corpusSize)
	threads := runtime.GOMAXPROCS(0)

	res := Result{Version: Version, Threads: threads}
	for i := 0; i < rounds; i++ {
		res.SingleThreadMBps = max(res.SingleThreadMBps, measure(corpus, 1, roundDuration))
	}
	for i := 0; i < rounds; i++ {
		res.MultiThreadMBps = max(res.MultiThreadMBps, measure(corpus, threads, roundDuration))
	}
	res.DurationSeconds = time.Since(start).Seconds()
	return res
}

// measure runs workers compressors flat out for d and returns their combined
// throughput in MB/s (10^6 bytes, like a disk or network rate).
func measure(corpus []byte, workers int, d time.Duration) float64 {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
	)
	begin := time.Now()
	deadline := begin.Add(d)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := compressUntil(corpus, deadline)
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	return float64(total) / time.Since(begin).Seconds() / 1e6
}

// compressUntil compresses the corpus repeatedly until the deadline and
// returns how many input bytes it got through. Each worker owns its writer and
// buffer, so the only shared state is the read-only corpus.
func compressUntil(corpus []byte, deadline time.Time) int {
	var buf bytes.Buffer
	buf.Grow(len(corpus))
	fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)

	n := 0
	for time.Now().Before(deadline) {
		buf.Reset()
		fw.Reset(&buf)
		fw.Write(corpus)
		fw.Close()
		n += len(corpus)
	}
	return n
}

// makeCorpus builds text-like input from a fixed vocabulary and a fixed seed.
// Random bytes would not compress and would mostly time the incompressible
// path; repeated text would time almost nothing. Words drawn at random from a
// small dictionary sit in between, like logs or source code, and the fixed
// seed makes every host compress exactly the same bytes.
func makeCorpus(size int) []byte {
	words := []string{
		"package", "update", "kernel", "system", "the", "a", "of", "and",
		"server", "client", "reboot", "check", "install", "version", "error",
		"config", "network", "memory", "disk", "process", "service", "log",
		"0", "1", "42", "2026", "x86_64", "aarch64", "rpm", "dnf", "apt",
	}
	seed := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 {
		// xorshift64*: tiny, deterministic, and identical on every platform.
		seed ^= seed >> 12
		seed ^= seed << 25
		seed ^= seed >> 27
		return seed * 2685821657736338717
	}

	out := make([]byte, 0, size+16)
	for len(out) < size {
		r := next()
		out = append(out, words[r%uint64(len(words))]...)
		switch r >> 60 {
		case 0:
			out = append(out, '\n')
		case 1:
			out = append(out, ", "...)
		default:
			out = append(out, ' ')
		}
	}
	return out[:size]
}
