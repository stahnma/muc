package hostinfo

import (
	"os"
	"path/filepath"
	"strings"
)

// Disk media kinds, as the dashboard shows them. They are about what the
// root filesystem sits on, which is what decides how fast the host feels at
// boot, at package installs and at anything that touches /var.
const (
	DiskMediaNVMe = "nvme"
	DiskMediaSSD  = "ssd"
	DiskMediaHDD  = "hdd"
	// DiskMediaFlash is an SD card or eMMC: solid-state, but nothing like an
	// SSD, which is the distinction worth making on a Raspberry Pi.
	DiskMediaFlash = "flash"
	// DiskMediaVirtual is a paravirtualised disk whose backing store the
	// guest cannot see.
	DiskMediaVirtual = "virtual"
)

// sysBlock is where the kernel describes block devices. A variable so tests
// can point it at a fake tree.
var sysBlock = "/sys/class/block"

// diskMedia says what kind of device backs the block device at devPath (as
// /proc/mounts names it — /dev/mapper/rl-root, /dev/nvme0n1p3, /dev/sda1). It
// follows device-mapper and md layers down to the physical disks under them,
// and reports the slowest kind it finds there: a mirror of an SSD and an HDD
// is as slow as the HDD. It returns "" when it cannot tell, which is the case
// for anything that is not a local block device.
func diskMedia(devPath string) string {
	if !strings.HasPrefix(devPath, "/dev/") {
		return ""
	}
	// /dev/mapper names are symlinks to /dev/dm-N, the name sysfs knows.
	if resolved, err := filepath.EvalSymlinks(devPath); err == nil {
		devPath = resolved
	}
	return slowest(physicalMedia(filepath.Base(devPath), 0))
}

// physicalMedia returns the media kind of every physical disk under name.
func physicalMedia(name string, depth int) []string {
	// Stacked layers (LUKS on LVM on md) are a handful deep; anything beyond
	// that is a loop in a tree that should not have one.
	if depth > 8 {
		return nil
	}
	dir := filepath.Join(sysBlock, name)

	if slaves, err := os.ReadDir(filepath.Join(dir, "slaves")); err == nil && len(slaves) > 0 {
		var kinds []string
		for _, s := range slaves {
			kinds = append(kinds, physicalMedia(s.Name(), depth+1)...)
		}
		return kinds
	}

	// A partition has no queue of its own; its disk is the directory above it
	// in the real sysfs path.
	if _, err := os.Stat(filepath.Join(dir, "partition")); err == nil {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil
		}
		return physicalMedia(filepath.Base(filepath.Dir(real)), depth+1)
	}

	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	return []string{mediaOf(name, dir)}
}

// mediaOf classifies one whole disk.
func mediaOf(name, dir string) string {
	switch {
	case strings.HasPrefix(name, "nvme"):
		return DiskMediaNVMe
	case strings.HasPrefix(name, "mmcblk"):
		return DiskMediaFlash
	case strings.HasPrefix(name, "vd"), strings.HasPrefix(name, "xvd"):
		return DiskMediaVirtual
	}
	rot, err := os.ReadFile(filepath.Join(dir, "queue", "rotational"))
	if err != nil {
		return ""
	}
	if strings.TrimSpace(string(rot)) == "1" {
		return DiskMediaHDD
	}
	return DiskMediaSSD
}

// slowest picks the slowest of several media kinds, ignoring unknowns.
func slowest(kinds []string) string {
	rank := map[string]int{
		DiskMediaHDD: 1, DiskMediaFlash: 2, DiskMediaVirtual: 3, DiskMediaSSD: 4, DiskMediaNVMe: 5,
	}
	best := ""
	for _, k := range kinds {
		if rank[k] == 0 {
			continue
		}
		if best == "" || rank[k] < rank[best] {
			best = k
		}
	}
	return best
}
