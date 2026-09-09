package runlog

import (
	"strings"
	"testing"
)

// drain reads everything currently queued, so a test can assert on what was
// broadcast without blocking on an empty channel.
func drain(s *Store) []Chunk {
	var got []Chunk
	for {
		select {
		case c := <-s.Events():
			got = append(got, c)
		default:
			return got
		}
	}
}

func TestAppendBuffersAndBroadcasts(t *testing.T) {
	s := New()

	s.Append("smallboi", "run1", 1, "upd: refreshing\n")
	s.Append("smallboi", "run1", 2, "upd: upgrading\n")

	snapshot, ok := s.Snapshot("smallboi")
	if !ok {
		t.Fatal("Snapshot reports nothing held for a host that just streamed output")
	}
	if snapshot.Output != "upd: refreshing\nupd: upgrading\n" {
		t.Errorf("Output = %q, want the chunks joined in order", snapshot.Output)
	}
	if snapshot.ID != "run1" || snapshot.Seq != 2 {
		t.Errorf("Snapshot = %+v, want run1 at seq 2", snapshot)
	}

	events := drain(s)
	if len(events) != 2 {
		t.Fatalf("broadcast %d chunks, want 2", len(events))
	}
	// The dashboard shares this socket with system updates and tells them apart
	// by this field alone.
	if events[0].Type != "update_output" {
		t.Errorf("Type = %q, want %q", events[0].Type, "update_output")
	}
	if events[0].Hostname != "smallboi" {
		t.Errorf("Hostname = %q, want %q", events[0].Hostname, "smallboi")
	}
}

// TestAppendNewRunReplacesTheBuffer pins that a second run starts clean: output
// from the previous one would otherwise appear above it as though it were part
// of the same run.
func TestAppendNewRunReplacesTheBuffer(t *testing.T) {
	s := New()

	s.Append("smallboi", "run1", 1, "first run\n")
	s.Append("smallboi", "run2", 1, "second run\n")

	snapshot, _ := s.Snapshot("smallboi")
	if strings.Contains(snapshot.Output, "first run") {
		t.Errorf("Output = %q, want only the current run's output", snapshot.Output)
	}
	if snapshot.ID != "run2" {
		t.Errorf("ID = %q, want %q", snapshot.ID, "run2")
	}
}

// TestAppendKeepsTheTail bounds what one host can hold: a viewer wants the
// recent history, not a whole upgrade log held in memory.
func TestAppendKeepsTheTail(t *testing.T) {
	s := New()

	s.Append("smallboi", "run1", 1, strings.Repeat("a", bufferLimit))
	s.Append("smallboi", "run1", 2, "the end\n")

	snapshot, _ := s.Snapshot("smallboi")
	if len(snapshot.Output) > bufferLimit {
		t.Errorf("held %d bytes, want at most %d", len(snapshot.Output), bufferLimit)
	}
	if !strings.HasSuffix(snapshot.Output, "the end\n") {
		t.Error("the most recent output was dropped instead of the oldest")
	}
	if !snapshot.Truncated {
		t.Error("Truncated = false after dropping output")
	}
}

// TestAppendDoesNotBlockWhenNobodyIsWatching is the reason chunks are dropped
// rather than queued: the NATS handler calls Append, and blocking it would stall
// every other message the server receives.
func TestAppendDoesNotBlockWhenNobodyIsWatching(t *testing.T) {
	s := New()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < eventBuffer*3; i++ {
			s.Append("smallboi", "run1", i, "x")
		}
	}()

	<-done // the test hangs rather than fails if Append ever blocks

	if snapshot, _ := s.Snapshot("smallboi"); snapshot.Seq != eventBuffer*3-1 {
		t.Errorf("Seq = %d, want the buffer to have kept up even as chunks were dropped", snapshot.Seq)
	}
}

func TestSnapshotUnknownHost(t *testing.T) {
	if _, ok := New().Snapshot("ghost"); ok {
		t.Error("Snapshot reports output held for a host that never ran anything")
	}
}
