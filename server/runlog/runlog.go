// Package runlog holds the live output of update runs in flight.
//
// It is deliberately not storage: this is a running commentary, not a record.
// The run's own tail is persisted with the system when the run ends, and what
// lives here is the fuller, moment-to-moment output that a viewer watching the
// run wants and nobody needs afterwards. Keeping it out of bbolt also keeps a
// chatty upgrade from writing to disk hundreds of times.
package runlog

import (
	"log/slog"
	"sync"
)

const (
	// bufferLimit is how much of one run's output is held per host. Enough that
	// a viewer who opens the page mid-run sees the recent history, bounded so a
	// fleet of noisy hosts cannot grow the server without limit.
	bufferLimit = 64 * 1024

	// eventBuffer is the depth of the channel feeding the WebSocket broadcast.
	// Chunks are dropped rather than queued without limit if nothing is
	// draining them; a gap in a live view is recoverable, unbounded memory is
	// not. The sequence numbers make the gap visible.
	eventBuffer = 256
)

// Chunk is one piece of live output, as it goes out to the dashboard. Type is
// what lets the page tell these apart from the system updates that share the
// WebSocket.
type Chunk struct {
	Type     string `json:"type"`
	Hostname string `json:"hostname"`
	ID       string `json:"id"`
	Seq      int    `json:"seq"`
	Chunk    string `json:"chunk"`
}

// Snapshot is everything held for one host's current run, for a viewer that
// arrives after it started.
type Snapshot struct {
	ID        string `json:"id"`
	Seq       int    `json:"seq"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
}

type buffer struct {
	id        string
	seq       int
	data      []byte
	truncated bool
}

// Store keeps the live output of the most recent run on each host.
type Store struct {
	mu     sync.Mutex
	runs   map[string]*buffer
	events chan Chunk
}

func New() *Store {
	return &Store{
		runs:   make(map[string]*buffer),
		events: make(chan Chunk, eventBuffer),
	}
}

// Append records a chunk and hands it to whoever is watching. A chunk carrying
// a run id the host was not previously running starts the buffer over, which is
// how the output of a new run replaces the last one's.
func (s *Store) Append(hostname, id string, seq int, chunk string) {
	s.mu.Lock()
	buf, ok := s.runs[hostname]
	if !ok || buf.id != id {
		buf = &buffer{id: id}
		s.runs[hostname] = buf
	}
	buf.seq = seq
	buf.data = append(buf.data, chunk...)
	if len(buf.data) > bufferLimit {
		buf.truncated = true
		buf.data = append([]byte(nil), buf.data[len(buf.data)-bufferLimit:]...)
	}
	s.mu.Unlock()

	select {
	case s.events <- Chunk{Type: "update_output", Hostname: hostname, ID: id, Seq: seq, Chunk: chunk}:
	default:
		slog.Warn("Live output channel is full; dropping a chunk", "hostname", hostname, "seq", seq)
	}
}

// Snapshot returns what is held for a host, and whether anything is.
func (s *Store) Snapshot(hostname string) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf, ok := s.runs[hostname]
	if !ok {
		return Snapshot{}, false
	}
	return Snapshot{
		ID:        buf.id,
		Seq:       buf.seq,
		Output:    string(buf.data),
		Truncated: buf.truncated,
	}, true
}

// Events is the stream of chunks to broadcast. One reader is expected.
func (s *Store) Events() <-chan Chunk {
	return s.events
}
