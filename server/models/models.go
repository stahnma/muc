package models

import "errors"

// ErrHostNotListening is returned when a command request reaches nobody: the
// host is offline, its client is too old to know the subject, or — for update
// commands — it has not opted into remote updates. It lives here so the
// transport and the HTTP layer can agree on it without one importing the other.
var ErrHostNotListening = errors.New("host is not listening for commands")

type System struct {
	Hostname            string   `json:"hostname"`
	Architecture        string   `json:"architecture"`
	Ip                  string   `json:"ip"`
	OS                  string   `json:"os"`
	OSVersion           string   `json:"os_version"`
	UpdatesAvailable    bool     `json:"updates_available"`
	UpdateStatusUnknown bool     `json:"update_status_unknown"`
	LastSeen            string   `json:"last_seen"`
	ClientVersion       string   `json:"client_version"`
	PendingUpdates      []Update `json:"pending_updates"`
	// UpdatesCheckedAt is when the client actually queried its package manager,
	// in UTC. LastSeen above is stamped by the server on receipt and so only
	// means "the host is reachable"; this is what says how old the update data
	// itself is. The two diverge when a client keeps checking in while its
	// package-manager metadata goes stale.
	UpdatesCheckedAt string `json:"updates_checked_at"`
	// UpdateCheckWarnings describes ways the check was incomplete but did not
	// outright fail — repositories the package manager silently skipped, for
	// instance. The pending-update count is then a lower bound.
	UpdateCheckWarnings []string `json:"update_check_warnings,omitempty"`
	CPUModel            string   `json:"cpu_model"`
	CPUCores            int      `json:"cpu_cores"`
	MemoryTotalBytes    uint64   `json:"memory_total_bytes"`
	UptimeSeconds       uint64   `json:"uptime_seconds"`
	RebootRequired      bool     `json:"reboot_required"`
	// Tailscale is nil until a host reports tailnet membership, and stays
	// non-nil afterwards — see MergeTailscale.
	Tailscale *Tailscale `json:"tailscale,omitempty"`
	// RemoteUpdatesEnabled is reported by the client: it says the host opted
	// into being patched from the dashboard and has an update command to run.
	// The server offers the button only where this and its own remote_updates
	// setting agree.
	RemoteUpdatesEnabled bool `json:"remote_updates_enabled"`
	// LastUpdateRun is the most recent dashboard-triggered update run, or nil
	// where none has happened. It arrives on its own subject rather than in the
	// check-in, so the subscriber carries it across check-ins.
	LastUpdateRun *UpdateRun `json:"last_update_run,omitempty"`
}

// UpdateRun records one dashboard-triggered update run on a host. The client
// publishes it twice — once when the run starts and once when it ends — and its
// JSON must stay in step with the client's updateRun.
type UpdateRun struct {
	ID     string `json:"id"`
	Status string `json:"status"` // running, succeeded, or failed
	// RequestedBy is the address the dashboard request came from, so the
	// journal and the UI can say who asked.
	RequestedBy string `json:"requested_by,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
	ExitCode    int    `json:"exit_code"`
	Error       string `json:"error,omitempty"`
	// Output is the tail of the command's combined output — enough to see what
	// went wrong without carrying a whole upgrade log.
	Output string `json:"output,omitempty"`
}

// UpdateAck is a host's immediate answer to an update request: it either took
// the job or said why not.
type UpdateAck struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Command  string `json:"command,omitempty"`
}

// CheckInAck is a host's immediate answer to a check-in request. It says only
// that the host will check in: the check-in itself arrives separately, on the
// ordinary check-in subject, because a package-manager query can take longer
// than anyone is willing to hold a request open for.
type CheckInAck struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

type Update struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
}
