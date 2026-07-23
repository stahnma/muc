// Package hostinfo collects hardware and runtime facts about the local
// machine (CPU, memory, uptime) plus a best-effort "reboot required" signal.
package hostinfo

import (
	"log/slog"
	"strings"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
)

// Info holds hardware and runtime facts about the local system.
type Info struct {
	CPUModel         string
	CPUCores         int
	MemoryTotalBytes uint64
	UptimeSeconds    uint64
	RebootRequired   bool
}

// Collect gathers hardware, uptime, and reboot-required information. Every
// probe is best-effort: a failure is logged and the corresponding field is
// left at its zero value so a partial result is still published rather than
// failing the whole check-in.
func Collect() Info {
	var info Info

	if cpus, err := cpu.Info(); err != nil {
		slog.Warn("Failed to read CPU info", "error", err)
	} else if len(cpus) > 0 {
		info.CPUModel = strings.TrimSpace(cpus[0].ModelName)
	}

	if cores, err := cpu.Counts(true); err != nil {
		slog.Warn("Failed to read CPU core count", "error", err)
	} else {
		info.CPUCores = cores
	}

	if vm, err := mem.VirtualMemory(); err != nil {
		slog.Warn("Failed to read memory info", "error", err)
	} else {
		info.MemoryTotalBytes = vm.Total
	}

	if uptime, err := host.Uptime(); err != nil {
		slog.Warn("Failed to read uptime", "error", err)
	} else {
		info.UptimeSeconds = uptime
	}

	info.RebootRequired = RebootRequired()

	return info
}
