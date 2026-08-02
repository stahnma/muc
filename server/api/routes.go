package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"server/models"
	"server/storage"
	"strings"

	"github.com/gorilla/mux"
)

type SystemSummary struct {
	Hostname            string          `json:"hostname"`
	Architecture        string          `json:"architecture"`
	Ip                  string          `json:"ip"`
	OS                  string          `json:"os"`
	OSVersion           string          `json:"os_version"`
	UpdatesAvailable    bool            `json:"updates_available"`
	UpdateStatusUnknown bool            `json:"update_status_unknown"`
	LastSeen            string          `json:"last_seen"`
	UpdatesCheckedAt    string          `json:"updates_checked_at"`
	UpdateCheckWarnings []string        `json:"update_check_warnings,omitempty"`
	PendingUpdates      []models.Update `json:"pending_updates"`
	CPUModel            string          `json:"cpu_model"`
	CPUCores            int             `json:"cpu_cores"`
	MemoryTotalBytes    uint64          `json:"memory_total_bytes"`
	UptimeSeconds       uint64          `json:"uptime_seconds"`
	RebootRequired      bool            `json:"reboot_required"`
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
