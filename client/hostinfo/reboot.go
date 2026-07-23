package hostinfo

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// rebootRequiredFiles are the flag files distributions in the Debian/Ubuntu
// family create (via update-notifier-common) when a reboot is pending.
var rebootRequiredFiles = []string{
	"/var/run/reboot-required",
	"/run/reboot-required",
}

// installedKernelPackages are the rpm package names that represent the actual
// running kernel across the supported rpm-based distributions, in preference
// order (dnf/RHEL/Fedora first, then SUSE).
var installedKernelPackages = []string{"kernel-core", "kernel-default", "kernel"}

// RebootRequired reports whether the local system needs a reboot to finish
// applying updates. It is deliberately best-effort and conservative: any
// uncertainty (unsupported OS, missing tools, command errors) yields false so
// the UI only flags reboots we are confident about, never a false alarm.
func RebootRequired() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	return linuxRebootRequired()
}

func linuxRebootRequired() bool {
	// Debian/Ubuntu: update-notifier drops a flag file. Most reliable signal,
	// needs no external tooling.
	if anyFileExists(rebootRequiredFiles) {
		return true
	}

	// RHEL/Fedora family: needs-restarting -r (from dnf-utils/yum-utils) is the
	// authoritative check when installed. Exit 1 => reboot needed, 0 => not.
	if commandExists("needs-restarting") {
		if code, ok := runExitCode("needs-restarting", "-r"); ok {
			return code == 1
		}
	}

	// openSUSE/SLES: zypper needs-rebooting. Exit 102 => reboot needed, 0 => not.
	if commandExists("zypper") {
		if code, ok := runExitCode("zypper", "needs-rebooting"); ok {
			return code == 102
		}
	}

	// Fallback for rpm systems without the tooling above: a reboot is needed if
	// the newest installed kernel package is newer than the running kernel. This
	// only catches kernel-driven reboots (not glibc/systemd), so it can
	// under-report, which is the acceptable direction.
	return kernelRebootNeeded(runningKernel(), installedKernels())
}

// anyFileExists reports whether any of the given paths exists.
func anyFileExists(paths []string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// commandExists reports whether name is found on PATH.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// runExitCode runs a command and returns its exit code. ok is false if the
// command could not be started or was killed by a signal (as opposed to
// exiting with a status), so callers can tell "tool errored" apart from "tool
// answered".
func runExitCode(name string, args ...string) (code int, ok bool) {
	err := exec.Command(name, args...).Run()
	if err == nil {
		return 0, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// ExitCode returns -1 if the process was terminated by a signal.
		if c := exitErr.ExitCode(); c >= 0 {
			return c, true
		}
	}
	return 0, false
}

// runningKernel returns the running kernel release (e.g.
// "5.14.0-427.13.1.el9_4.x86_64"), matching the rpm VERSION-RELEASE.ARCH form.
func runningKernel() string {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// installedKernels returns the VERSION-RELEASE.ARCH strings of every installed
// kernel package, trying each known package name until one yields results.
func installedKernels() []string {
	for _, pkg := range installedKernelPackages {
		out, err := exec.Command("rpm", "-q", "--qf", "%{VERSION}-%{RELEASE}.%{ARCH}\n", pkg).Output()
		if err != nil {
			continue
		}
		var versions []string
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.Contains(line, "not installed") {
				versions = append(versions, line)
			}
		}
		if len(versions) > 0 {
			return versions
		}
	}
	return nil
}

// kernelRebootNeeded reports whether the newest installed kernel is strictly
// newer than the running kernel. It returns false when either input is missing
// so an inability to determine the kernels never produces a false alarm.
func kernelRebootNeeded(running string, installed []string) bool {
	if running == "" || len(installed) == 0 {
		return false
	}
	newest := newestVersion(installed)
	return newest != "" && compareVersions(newest, running) > 0
}
