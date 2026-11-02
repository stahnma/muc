// Package hostinfo collects hardware and runtime facts about the local
// machine (CPU, memory, disk, uptime) plus a best-effort "reboot required"
// signal.
package hostinfo

import (
	"log/slog"
	"strings"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
)

// Info holds hardware and runtime facts about the local system.
type Info struct {
	CPUModel string
	// CPUCores counts logical CPUs (threads); CPUPhysicalCores counts cores.
	// They differ by SMT, which is worth seeing when comparing hosts: 16
	// threads on 8 cores is not 16 cores.
	CPUCores         int
	CPUPhysicalCores int
	// CPUMaxMHz is the highest clock the CPU is rated for, where the kernel
	// says; 0 when it does not, as in most VMs.
	CPUMaxMHz float64
	// Virtualization names the hypervisor or container runtime this host is
	// a guest of ("kvm", "docker"), or is empty on bare metal.
	Virtualization       string
	MemoryTotalBytes     uint64
	MemoryAvailableBytes uint64
	// Disk* describe the root filesystem. DiskMedia is one of the DiskMedia*
	// constants, or empty when it cannot be told.
	DiskTotalBytes uint64
	DiskFreeBytes  uint64
	DiskMedia      string
	UptimeSeconds  uint64
	RebootRequired bool
	// Tailscale is nil for hosts with no tailnet membership to report.
	Tailscale *TailscaleInfo
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
		info.CPUMaxMHz = cpus[0].Mhz
	}

	if cores, err := cpu.Counts(true); err != nil {
		slog.Warn("Failed to read CPU core count", "error", err)
	} else {
		info.CPUCores = cores
	}

	if cores, err := cpu.Counts(false); err != nil {
		slog.Warn("Failed to read physical core count", "error", err)
	} else {
		info.CPUPhysicalCores = cores
	}

	if system, role, err := host.Virtualization(); err != nil {
		slog.Debug("Failed to detect virtualization", "error", err)
	} else if role == "guest" {
		info.Virtualization = system
	}

	if vm, err := mem.VirtualMemory(); err != nil {
		slog.Warn("Failed to read memory info", "error", err)
	} else {
		info.MemoryTotalBytes = vm.Total
		info.MemoryAvailableBytes = vm.Available
	}

	if usage, err := disk.Usage("/"); err != nil {
		slog.Warn("Failed to read root filesystem usage", "error", err)
	} else {
		info.DiskTotalBytes = usage.Total
		info.DiskFreeBytes = usage.Free
	}
	info.DiskMedia = rootDiskMedia()

	if uptime, err := host.Uptime(); err != nil {
		slog.Warn("Failed to read uptime", "error", err)
	} else {
		info.UptimeSeconds = uptime
	}

	info.RebootRequired = RebootRequired()
	info.Tailscale = TailscaleStatus()

	return info
}

// rootDiskMedia finds the device mounted at / and says what kind it is.
func rootDiskMedia() string {
	parts, err := disk.Partitions(false)
	if err != nil {
		slog.Debug("Failed to list mounts", "error", err)
		return ""
	}
	for _, p := range parts {
		if p.Mountpoint == "/" {
			return diskMedia(p.Device)
		}
	}
	return ""
}
