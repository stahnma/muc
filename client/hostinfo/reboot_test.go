package hostinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"5.14.0-427.13.1.el9_4.x86_64", "5.14.0-427.13.1.el9_4.x86_64", 0},
		{"5.14.0-427.13.1.el9_4.x86_64", "5.14.0-427.9.1.el9_4.x86_64", 1},  // 13 > 9 numerically
		{"5.14.0-427.9.1.el9_4.x86_64", "5.14.0-427.13.1.el9_4.x86_64", -1}, // reversed
		{"6.9.4-200.fc40.x86_64", "6.8.11-300.fc40.x86_64", 1},              // 9 > 8 minor
		{"5.14.0-427.13.1.el9_4.x86_64", "5.14.0-70.30.1.el9_0.x86_64", 1},  // 427 > 70
		{"427.13.1", "427.13", 1}, // more segments is newer
		{"1.0", "1.0", 0},
		{"6.10.0-1.fc41", "6.9.9-1.fc41", 1}, // 10 > 9 despite fewer digits after strip
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNewestVersion(t *testing.T) {
	installed := []string{
		"5.14.0-70.30.1.el9_0.x86_64",
		"5.14.0-427.13.1.el9_4.x86_64",
		"5.14.0-427.9.1.el9_4.x86_64",
	}
	want := "5.14.0-427.13.1.el9_4.x86_64"
	if got := newestVersion(installed); got != want {
		t.Errorf("newestVersion() = %q, want %q", got, want)
	}
	if got := newestVersion(nil); got != "" {
		t.Errorf("newestVersion(nil) = %q, want empty", got)
	}
}

func TestKernelRebootNeeded(t *testing.T) {
	running := "5.14.0-427.9.1.el9_4.x86_64"
	cases := []struct {
		name      string
		running   string
		installed []string
		want      bool
	}{
		{
			name:      "newer kernel installed",
			running:   running,
			installed: []string{running, "5.14.0-427.13.1.el9_4.x86_64"},
			want:      true,
		},
		{
			name:      "running is newest",
			running:   "5.14.0-427.13.1.el9_4.x86_64",
			installed: []string{running, "5.14.0-427.13.1.el9_4.x86_64"},
			want:      false,
		},
		{
			name:      "only running installed",
			running:   running,
			installed: []string{running},
			want:      false,
		},
		{
			name:      "no running kernel known",
			running:   "",
			installed: []string{running},
			want:      false,
		},
		{
			name:      "no installed kernels known",
			running:   running,
			installed: nil,
			want:      false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := kernelRebootNeeded(c.running, c.installed); got != c.want {
				t.Errorf("kernelRebootNeeded(%q, %v) = %v, want %v", c.running, c.installed, got, c.want)
			}
		})
	}
}

func TestAnyFileExists(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "reboot-required")
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nope")

	if !anyFileExists([]string{missing, present}) {
		t.Errorf("anyFileExists should find %q", present)
	}
	if anyFileExists([]string{missing}) {
		t.Errorf("anyFileExists should not find %q", missing)
	}
	if anyFileExists(nil) {
		t.Errorf("anyFileExists(nil) should be false")
	}
}

func TestRunExitCode(t *testing.T) {
	// Exit 0.
	if code, ok := runExitCode("true"); !ok || code != 0 {
		t.Errorf("runExitCode(true) = (%d, %v), want (0, true)", code, ok)
	}
	// Non-zero exit is still an answer, not an error.
	if code, ok := runExitCode("false"); !ok || code != 1 {
		t.Errorf("runExitCode(false) = (%d, %v), want (1, true)", code, ok)
	}
	// Missing command cannot be started: ok must be false.
	if _, ok := runExitCode("muc-no-such-command-xyz"); ok {
		t.Errorf("runExitCode(missing) ok = true, want false")
	}
}
