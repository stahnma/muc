package api

import (
	"log/slog"
	"net/http"
	"server/storage"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

const (
	defaultOperationsLimit = 100
	maxOperationsLimit     = 1000
)

// OperationsHandler serves the operations log, newest first: every update,
// reboot and check-in the dashboard asked for, and what each host said about
// it.
//
// ?host= narrows it to one host, ?limit= caps the page (100 by default, 1000
// at most), and ?before= takes the seq of the last entry already shown, to
// read further back.
func OperationsHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveOperations(w, r, store, strings.TrimSpace(r.URL.Query().Get("host")))
	}
}

// SystemOperationsHandler is OperationsHandler for the host in the path. A
// host that has since been removed still has its history, so an unknown
// hostname is an empty log rather than a 404.
func SystemOperationsHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostname := strings.TrimSpace(mux.Vars(r)["hostname"])
		if hostname == "" {
			writeAPIError(w, http.StatusBadRequest, "Hostname is required")
			return
		}
		serveOperations(w, r, store, hostname)
	}
}

func serveOperations(w http.ResponseWriter, r *http.Request, store storage.Storage, hostname string) {
	q := r.URL.Query()

	limit := defaultOperationsLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeAPIError(w, http.StatusBadRequest, "limit must be a positive whole number")
			return
		}
		limit = min(n, maxOperationsLimit)
	}

	var before uint64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "before must be the seq of a log entry")
			return
		}
		before = n
	}

	ops, err := store.GetOperations(hostname, before, limit)
	if err != nil {
		slog.Error("Failed to read the operations log", "hostname", hostname, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "Failed to read the operations log")
		return
	}
	writeJSON(w, http.StatusOK, ops)
}
