package hostinfo

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSysBlock builds a sysfs-shaped tree: whole disks under devices/, with
// partitions nested inside them, and class/block holding symlinks to both, as
// the kernel lays it out.
type fakeSysBlock struct {
	t    *testing.T
	root string
}

func newFakeSysBlock(t *testing.T) *fakeSysBlock {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "class", "block"), 0o755)
	old := sysBlock
	sysBlock = filepath.Join(root, "class", "block")
	t.Cleanup(func() { sysBlock = old })
	return &fakeSysBlock{t: t, root: root}
}

func (f *fakeSysBlock) disk(name, rotational string) {
	dir := filepath.Join(f.root, "devices", name)
	os.MkdirAll(filepath.Join(dir, "queue"), 0o755)
	os.WriteFile(filepath.Join(dir, "queue", "rotational"), []byte(rotational+"\n"), 0o644)
	os.Symlink(dir, filepath.Join(sysBlock, name))
}

func (f *fakeSysBlock) partition(disk, name string) {
	dir := filepath.Join(f.root, "devices", disk, name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "partition"), []byte("1\n"), 0o644)
	os.Symlink(dir, filepath.Join(sysBlock, name))
}

func (f *fakeSysBlock) mapper(name string, slaves ...string) {
	dir := filepath.Join(f.root, "devices", "virtual", name)
	os.MkdirAll(filepath.Join(dir, "slaves"), 0o755)
	for _, s := range slaves {
		os.Symlink(filepath.Join(sysBlock, s), filepath.Join(dir, "slaves", s))
	}
	os.Symlink(dir, filepath.Join(sysBlock, name))
}

func TestDiskMedia(t *testing.T) {
	f := newFakeSysBlock(t)
	f.disk("nvme0n1", "0")
	f.partition("nvme0n1", "nvme0n1p3")
	f.mapper("dm-0", "nvme0n1p3")
	f.disk("sda", "1")
	f.partition("sda", "sda1")
	f.disk("sdb", "0")
	f.partition("sdb", "sdb1")
	f.disk("mmcblk0", "0")
	f.partition("mmcblk0", "mmcblk0p2")
	f.disk("vda", "1")
	f.partition("vda", "vda4")
	f.mapper("md0", "sda1", "sdb1")

	tests := []struct {
		dev  string
		want string
	}{
		{"/dev/dm-0", DiskMediaNVMe}, // LVM on an NVMe partition
		{"/dev/sda1", DiskMediaHDD},
		{"/dev/sdb1", DiskMediaSSD},
		{"/dev/mmcblk0p2", DiskMediaFlash}, // a Raspberry Pi's SD card
		{"/dev/vda4", DiskMediaVirtual},    // virtio reports rotational, and means nothing by it
		{"/dev/md0", DiskMediaHDD},         // a mirror is as slow as its slowest leg
		{"/dev/nosuch", ""},
		{"tmpfs", ""},
		{"server:/export", ""},
	}
	for _, tt := range tests {
		if got := diskMedia(tt.dev); got != tt.want {
			t.Errorf("diskMedia(%q) = %q, want %q", tt.dev, got, tt.want)
		}
	}
}
