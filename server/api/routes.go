package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"server/models"
	"server/runlog"
	"server/storage"
	"strings"

	"github.com/gorilla/mux"
)

type SystemSummary struct {
	Hostname            string            `json:"hostname"`
	Architecture        string            `json:"architecture"`
	Ip                  string            `json:"ip"`
	OS                  string            `json:"os"`
	OSVersion           string            `json:"os_version"`
	UpdatesAvailable    bool              `json:"updates_available"`
	UpdateStatusUnknown bool              `json:"update_status_unknown"`
	LastSeen            string            `json:"last_seen"`
	UpdatesCheckedAt    string            `json:"updates_checked_at"`
	UpdateCheckWarnings []string          `json:"update_check_warnings,omitempty"`
	PendingUpdates      []models.Update   `json:"pending_updates"`
	CPUModel            string            `json:"cpu_model"`
	CPUCores            int               `json:"cpu_cores"`
	MemoryTotalBytes    uint64            `json:"memory_total_bytes"`
	UptimeSeconds       uint64            `json:"uptime_seconds"`
	RebootRequired      bool              `json:"reboot_required"`
	Tailscale           *models.Tailscale `json:"tailscale,omitempty"`
	// RemoteUpdatesEnabled is the host's own opt-in, reported at check-in. The
	// dashboard needs it in the list response so it can decide per row whether
	// to offer the update button.
	RemoteUpdatesEnabled bool              `json:"remote_updates_enabled"`
	LastUpdateRun        *models.UpdateRun `json:"last_update_run,omitempty"`
}

// UpdateRequester asks one host to install its pending packages, returning the
// host's own answer. Implemented by the NATS connection; nil when the server is
// not configured to allow remote updates.
type UpdateRequester interface {
	RequestUpdate(hostname, requestedBy string) (models.UpdateAck, error)
}

// CheckInRequester asks one host to publish a fresh check-in, returning the
// host's own answer. Implemented by the NATS connection; nil only when there is
// no connection to ask over, since check-ins are not gated by configuration.
type CheckInRequester interface {
	RequestCheckIn(hostname, requestedBy string) (models.CheckInAck, error)
}

// GetSystemsHandler returns a JSON list of systems with pending updates details
func GetSystemsHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Retrieve all systems from storage
		systems, err := store.GetAllSystems()
		if err != nil {
			slog.Error("Failed to get all systems", "error", err)
			http.Error(w, "Failed to fetch systems", http.StatusInternalServerError)
			return
		}

		// Generate summarized system data
		summaries := make([]SystemSummary, 0, len(systems))
		for _, system := range systems {
			summaries = append(summaries, SystemSummary{
				Hostname:            system.Hostname,
				Architecture:        system.Architecture,
				Ip:                  system.Ip,
				OS:                  system.OS,
				OSVersion:           system.OSVersion,
				UpdatesAvailable:    system.UpdatesAvailable,
				UpdateStatusUnknown: system.UpdateStatusUnknown,
				LastSeen:            system.LastSeen,
				UpdatesCheckedAt:    system.UpdatesCheckedAt,
				UpdateCheckWarnings: system.UpdateCheckWarnings,
				PendingUpdates:      system.PendingUpdates,
				CPUModel:            system.CPUModel,
				CPUCores:            system.CPUCores,
				MemoryTotalBytes:    system.MemoryTotalBytes,
				UptimeSeconds:       system.UptimeSeconds,
				RebootRequired:      system.RebootRequired,
				Tailscale:           system.Tailscale,

				RemoteUpdatesEnabled: system.RemoteUpdatesEnabled,
				LastUpdateRun:        system.LastUpdateRun,
			})
		}

		// Encode to buffer first to check for errors before writing headers
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(summaries); err != nil {
			slog.Error("Failed to encode systems summary", "error", err)
			http.Error(w, "Failed to encode systems summary", http.StatusInternalServerError)
			return
		}

		// Headers can now be safely set since encoding succeeded
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(buf.Bytes()); err != nil {
			slog.Error("Failed to write response", "error", err)
		}
	}
}

// GetSystemHandler returns detailed information for a single system as JSON
func GetSystemHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get the hostname from the URL
		vars := mux.Vars(r)
		hostname := strings.TrimSpace(vars["hostname"])

		// Retrieve the specific system from storage
		system, err := store.GetSystem(hostname)
		if err != nil {
			slog.Error("Failed to get system", "hostname", hostname, "error", err)
			http.Error(w, "System not found", http.StatusNotFound)
			return
		}

		// Encode to buffer first to check for errors before writing headers
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(system); err != nil {
			slog.Error("Failed to encode system details", "error", err)
			http.Error(w, "Failed to encode system details", http.StatusInternalServerError)
			return
		}

		// Headers can now be safely set since encoding succeeded
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(buf.Bytes()); err != nil {
			slog.Error("Failed to write response", "error", err)
		}
	}
}

// DeleteSystemHandler deletes a system from storage
func DeleteSystemHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get the hostname from the URL
		vars := mux.Vars(r)
		hostname := strings.TrimSpace(vars["hostname"])

		if hostname == "" {
			http.Error(w, "Hostname is required", http.StatusBadRequest)
			return
		}

		// Delete the system from storage
		err := store.DeleteSystem(hostname)
		if err != nil {
			slog.Error("Failed to delete system", "hostname", hostname, "error", err)
			// Check if it's a "not found" error
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, "System not found", http.StatusNotFound)
			} else {
				http.Error(w, "Failed to delete system", http.StatusInternalServerError)
			}
			return
		}

		// Return success response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":   "success",
			"message":  "System deleted successfully",
			"hostname": hostname,
		})
	}
}

// FeaturesHandler tells the dashboard which optional capabilities this server
// offers, so the UI can hide controls that would only ever return an error.
func FeaturesHandler(updater UpdateRequester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]bool{
			"remote_updates": updater != nil,
		}); err != nil {
			slog.Error("Failed to write features response", "error", err)
		}
	}
}

// RunUpdateHandler asks a host to install its pending packages.
//
// It answers as soon as the host has accepted or refused the job, not when the
// run finishes: the run reports its own progress back over NATS and reaches the
// dashboard as an ordinary system update. Both sides must have opted in — the
// server through remote_updates, the host through allow_remote_updates — and
// the host's opt-in is the one that actually gates anything, since a client
// that has not opted in never subscribes to the command subject.
func RunUpdateHandler(store storage.Storage, updater UpdateRequester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if updater == nil {
			writeAPIError(w, http.StatusForbidden,
				"Remote updates are disabled on this server (set remote_updates: true to enable them)")
			return
		}

		vars := mux.Vars(r)
		hostname := strings.TrimSpace(vars["hostname"])
		if hostname == "" {
			writeAPIError(w, http.StatusBadRequest, "Hostname is required")
			return
		}

		system, err := store.GetSystem(hostname)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "System not found")
			return
		}
		if !system.RemoteUpdatesEnabled {
			writeAPIError(w, http.StatusConflict,
				"This host has not opted into remote updates (set allow_remote_updates: true in its client config)")
			return
		}

		ack, err := updater.RequestUpdate(hostname, requesterAddress(r))
		switch {
		case errors.Is(err, models.ErrHostNotListening):
			// The stored opt-in said yes but nothing answered, so the host is
			// down or its client has since been reconfigured.
			writeAPIError(w, http.StatusServiceUnavailable,
				"No response from "+hostname+": it is offline, or its client is no longer accepting update commands")
			return
		case err != nil:
			slog.Error("Update request failed", "hostname", hostname, "error", err)
			writeAPIError(w, http.StatusBadGateway, "Update request failed: "+err.Error())
			return
		}

		if !ack.Accepted {
			reason := ack.Reason
			if reason == "" {
				reason = "the host refused the update request"
			}
			writeAPIError(w, http.StatusConflict, reason)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if err := json.NewEncoder(w).Encode(map[string]string{
			"status":   "accepted",
			"hostname": hostname,
			"id":       ack.ID,
			"command":  ack.Command,
			"message":  "Update started on " + hostname,
		}); err != nil {
			slog.Error("Failed to write update response", "error", err)
		}
	}
}

// CheckInHandler asks a host to collect its state and publish it now, instead
// of waiting out its poll interval.
//
// Nothing configured gates it. A check-in installs nothing and republishes only
// what the client sends every few minutes anyway, so there is no server flag and
// no host opt-in to consult — every client listens for the command. The limit
// that does exist is the host's own minimum gap between commands, which comes
// back as a refusal and is reported here as a 409.
//
// Like the update route it answers when the host has accepted, not when the
// check has run: the check-in arrives separately and reaches the dashboard as an
// ordinary system update.
func CheckInHandler(store storage.Storage, requester CheckInRequester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if requester == nil {
			writeAPIError(w, http.StatusServiceUnavailable,
				"Check-in requests are unavailable: the server has no NATS connection")
			return
		}

		vars := mux.Vars(r)
		hostname := strings.TrimSpace(vars["hostname"])
		if hostname == "" {
			writeAPIError(w, http.StatusBadRequest, "Hostname is required")
			return
		}

		if _, err := store.GetSystem(hostname); err != nil {
			writeAPIError(w, http.StatusNotFound, "System not found")
			return
		}

		ack, err := requester.RequestCheckIn(hostname, requesterAddress(r))
		switch {
		case errors.Is(err, models.ErrHostNotListening):
			// Every current client subscribes, so this is a host that is down
			// or one running a client from before the command existed.
			writeAPIError(w, http.StatusServiceUnavailable,
				"No response from "+hostname+": it is offline, or its client is too old to accept check-in requests")
			return
		case err != nil:
			slog.Error("Check-in request failed", "hostname", hostname, "error", err)
			writeAPIError(w, http.StatusBadGateway, "Check-in request failed: "+err.Error())
			return
		}

		if !ack.Accepted {
			reason := ack.Reason
			if reason == "" {
				reason = "the host refused the check-in request"
			}
			writeAPIError(w, http.StatusConflict, reason)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if err := json.NewEncoder(w).Encode(map[string]string{
			"status":   "accepted",
			"hostname": hostname,
			"id":       ack.ID,
			"message":  hostname + " is checking in",
		}); err != nil {
			slog.Error("Failed to write check-in response", "error", err)
		}
	}
}

// writeAPIError returns a JSON error, which is what the dashboard's fetch
// handler reads to show the operator why nothing happened.
func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		slog.Error("Failed to write error response", "error", err)
	}
}

// requesterAddress is recorded with the run so the host's journal and the
// dashboard can both say where the request came from. It is provenance for a
// home network, not authentication.
func requesterAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// UpdateOutputHandler serves the live output of the run on one host, for a
// viewer that arrived after it started — a page opened or reloaded mid-run
// would otherwise show an empty pane until the next chunk happened to arrive.
//
// This is the output held in memory for the run in flight. What survives a
// server restart is the tail on the run record, in GET /api/systems/{hostname}.
func UpdateOutputHandler(runs *runlog.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		hostname := strings.TrimSpace(vars["hostname"])

		snapshot, ok := runs.Snapshot(hostname)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "No live output for "+hostname)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(snapshot); err != nil {
			slog.Error("Failed to write update output response", "error", err)
		}
	}
}
