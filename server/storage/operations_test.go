package storage

import (
	"server/models"
	"testing"
)

func appendOps(t *testing.T, store *BboltStorage, hosts ...string) {
	t.Helper()
	for _, h := range hosts {
		if err := store.AppendOperation(models.Operation{Hostname: h, Action: "update", Event: models.OperationRequested}); err != nil {
			t.Fatalf("AppendOperation(%s): %v", h, err)
		}
	}
}

func seqs(ops []models.Operation) []uint64 {
	out := make([]uint64, len(ops))
	for i, op := range ops {
		out[i] = op.Seq
	}
	return out
}

func sameSeqs(got []models.Operation, want ...uint64) bool {
	g := seqs(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

func TestOperationsEmpty(t *testing.T) {
	store := newTestStore(t)
	ops, err := store.GetOperations("", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ops == nil || len(ops) != 0 {
		t.Errorf("GetOperations on an empty log = %#v, want an empty, non-nil slice", ops)
	}
}

// TestOperationsNewestFirst covers the order, the stamping, and the limit.
func TestOperationsNewestFirst(t *testing.T) {
	store := newTestStore(t)
	appendOps(t, store, "a", "b", "c", "d")

	ops, err := store.GetOperations("", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSeqs(ops, 4, 3, 2) {
		t.Errorf("seqs = %v, want [4 3 2]", seqs(ops))
	}
	if ops[0].Hostname != "d" || ops[0].Time == "" {
		t.Errorf("newest entry = %+v, want host d with a time stamped", ops[0])
	}
}

func TestOperationsFilterByHost(t *testing.T) {
	store := newTestStore(t)
	appendOps(t, store, "a", "b", "a", "c", "a")

	ops, err := store.GetOperations("a", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSeqs(ops, 5, 3, 1) {
		t.Errorf("seqs for host a = %v, want [5 3 1]", seqs(ops))
	}
}

// TestOperationsBefore pages back through the log, including from a cursor
// that is no longer in it.
func TestOperationsBefore(t *testing.T) {
	store := newTestStore(t)
	appendOps(t, store, "a", "b", "c", "d", "e")

	for _, tc := range []struct {
		before uint64
		want   []uint64
	}{
		{before: 4, want: []uint64{3, 2}},
		{before: 1, want: []uint64{}},
		{before: 99, want: []uint64{5, 4}},
	} {
		ops, err := store.GetOperations("", tc.before, 2)
		if err != nil {
			t.Fatal(err)
		}
		if !sameSeqs(ops, tc.want...) {
			t.Errorf("before %d: seqs = %v, want %v", tc.before, seqs(ops), tc.want)
		}
	}
}

func TestOperationsPruned(t *testing.T) {
	kept := operationsKept
	operationsKept = 3
	t.Cleanup(func() { operationsKept = kept })

	store := newTestStore(t)
	appendOps(t, store, "a", "b", "c", "d", "e", "f")

	ops, err := store.GetOperations("", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSeqs(ops, 6, 5, 4) {
		t.Errorf("seqs after pruning = %v, want [6 5 4]", seqs(ops))
	}
}
