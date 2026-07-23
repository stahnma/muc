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
	CPUModel            string   `json:"cpu_model"`
	CPUCores            int      `json:"cpu_cores"`
	MemoryTotalBytes    uint64   `json:"memory_total_bytes"`
	UptimeSeconds       uint64   `json:"uptime_seconds"`
	RebootRequired      bool     `json:"reboot_required"`
}

type Update struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
}
