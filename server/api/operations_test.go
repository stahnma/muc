package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"server/models"
	"testing"

	"github.com/gorilla/mux"
)

func getOperations(t *testing.T, handler http.HandlerFunc, target string, vars map[string]string) (*httptest.ResponseRecorder, []models.Operation) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if vars != nil {
		req = mux.SetURLVars(req, vars)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	var ops []models.Operation
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &ops); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body.String(), err)
		}
	}
	return rec, ops
}

func logStore() *fakeStorage {
	store := &fakeStorage{}
	for _, h := range []string{"a", "b", "a", "b", "a"} {
		store.AppendOperation(models.Operation{Hostname: h})
	}
	return store
}

func TestOperationsHandler(t *testing.T) {
	store := logStore()

	for _, tc := range []struct {
		target string
		want   []uint64
	}{
		{"/api/operations", []uint64{5, 4, 3, 2, 1}},
		{"/api/operations?limit=2", []uint64{5, 4}},
		{"/api/operations?host=a", []uint64{5, 3, 1}},
		{"/api/operations?host=a&before=5&limit=1", []uint64{3}},
	} {
		rec, ops := getOperations(t, OperationsHandler(store), tc.target, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", tc.target, rec.Code)
			continue
		}
		var got []uint64
		for _, op := range ops {
			got = append(got, op.Seq)
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: seqs %v, want %v", tc.target, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: seqs %v, want %v", tc.target, got, tc.want)
				break
			}
		}
	}
}

func TestOperationsHandlerBadParams(t *testing.T) {
	for _, target := range []string{
		"/api/operations?limit=0",
		"/api/operations?limit=lots",
		"/api/operations?before=-1",
	} {
		rec, _ := getOperations(t, OperationsHandler(logStore()), target, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", target, rec.Code)
		}
	}
}

// TestSystemOperationsHandler: the per-host route is the host filter, and a
// host nobody knows any more is an empty history, not a 404.
func TestSystemOperationsHandler(t *testing.T) {
	store := logStore()

	rec, ops := getOperations(t, SystemOperationsHandler(store), "/api/systems/b/operations", map[string]string{"hostname": "b"})
	if rec.Code != http.StatusOK || len(ops) != 2 || ops[0].Seq != 4 {
		t.Errorf("host b: status %d, ops %+v; want 200 and seqs [4 2]", rec.Code, ops)
	}

	rec, ops = getOperations(t, SystemOperationsHandler(store), "/api/systems/gone/operations", map[string]string{"hostname": "gone"})
	if rec.Code != http.StatusOK || ops == nil || len(ops) != 0 {
		t.Errorf("unknown host: status %d, ops %#v; want 200 and []", rec.Code, ops)
	}
}
