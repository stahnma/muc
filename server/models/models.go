package models

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
}

type Update struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
}
