package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func writeScript(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveUpdateCommandConfigured covers the configured path being honoured
// exactly: falling back to a discovered script would run something other than
// what the administrator asked for.
func TestResolveUpdateCommandConfigured(t *testing.T) {
	path := writeScript(t, t.TempDir(), "upd", 0755)

	got, err := resolveUpdateCommand(path)
	if err != nil {
		t.Fatalf("resolveUpdateCommand(%q) returned %v", path, err)
	}
	if got != path {
		t.Errorf("resolved %q, want %q", got, path)
	}
}

// TestResolveUpdateCommandConfiguredUnusable pins the refusal: a configured
// command that is missing or not executable is an error, not a reason to look
// elsewhere. The client then leaves the capability unadvertised.
func TestResolveUpdateCommandConfiguredUnusable(t *testing.T) {
	dir := t.TempDir()

	for name, path := range map[string]string{
		"missing":        filepath.Join(dir, "nosuchfile"),
		"not executable": writeScript(t, dir, "notexec", 0644),
		"directory":      dir,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := resolveUpdateCommand(path); err == nil {
				t.Errorf("resolveUpdateCommand(%q) = %q, want an error", path, got)
			}
		})
	}
}

// withoutPackagedCandidates points the search away from the real filesystem, so
// these tests describe the lookup rather than whatever the host running them
// happens to have installed.
func withoutPackagedCandidates(t *testing.T, candidates ...string) {
	t.Helper()
	original := updateCommandCandidates
	updateCommandCandidates = candidates
	t.Cleanup(func() { updateCommandCandidates = original })
}

// TestResolveUpdateCommandNoneFound checks the message names what was tried;
// this error is what an operator sees in the journal after opting in on a host
// where upd was never installed.
func TestResolveUpdateCommandNoneFound(t *testing.T) {
	withoutPackagedCandidates(t)
	t.Setenv("PATH", t.TempDir())

	_, err := resolveUpdateCommand("")
	if err == nil {
		t.Fatal("resolveUpdateCommand(\"\") succeeded with no upd installed anywhere")
	}
	if !strings.Contains(err.Error(), "update_command") {
		t.Errorf("error %q does not say how to fix it", err)
	}
}

// TestResolveUpdateCommandPrefersPackagedLocation pins the search order: the
// packaged script wins over one that merely happens to be on PATH.
func TestResolveUpdateCommandPrefersPackagedLocation(t *testing.T) {
	packagedDir, pathDir := t.TempDir(), t.TempDir()
	packaged := writeScript(t, packagedDir, "upd", 0755)
	writeScript(t, pathDir, "upd", 0755)
	withoutPackagedCandidates(t, packaged)
	t.Setenv("PATH", pathDir)

	got, err := resolveUpdateCommand("")
	if err != nil {
		t.Fatalf("resolveUpdateCommand(\"\") returned %v", err)
	}
	if got != packaged {
		t.Errorf("resolved %q, want the packaged %q", got, packaged)
	}
}

// TestResolveUpdateCommandFromPath covers the hand-installed script: hosts had
// upd on PATH long before the package shipped one.
func TestResolveUpdateCommandFromPath(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "upd", 0755)
	withoutPackagedCandidates(t)
	t.Setenv("PATH", dir)

	got, err := resolveUpdateCommand("")
	if err != nil {
		t.Fatalf("resolveUpdateCommand(\"\") returned %v", err)
	}
	if got != path {
		t.Errorf("resolved %q, want %q", got, path)
	}
}

// TestTailWriterKeepsTheEnd pins the half of the output that matters: a failing
// upgrade says why on its last lines, and the whole log would not fit in a NATS
// message.
func TestTailWriterKeepsTheEnd(t *testing.T) {
	w := &tailWriter{limit: 10}

	for _, chunk := range []string{"aaaaaaaaaa", "bbbbb", "12345"} {
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil", chunk, n, err, len(chunk))
		}
	}

	got := w.String()
	if !strings.HasSuffix(got, "bbbbb12345") {
		t.Errorf("output = %q, want it to end with the last 10 bytes written", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("output = %q, want it to say that earlier output was dropped", got)
	}
}

func TestTailWriterUnderLimitIsVerbatim(t *testing.T) {
	w := &tailWriter{limit: 1024}
	w.Write([]byte("upd: done\n"))

	if got := w.String(); got != "upd: done\n" {
		t.Errorf("output = %q, want it unchanged when it fits", got)
	}
}

// TestUpdateArgvWrapsInSystemdRun documents why the run does not simply fork
// from the daemon: muc-client.service makes /usr read-only, and restarting the
// unit (which a transaction upgrading muc-client does) would kill the package
// manager mid-write.
func TestUpdateArgvWrapsInSystemdRun(t *testing.T) {
	argv := updateArgv("/usr/libexec/muc/upd", "muc-update-test")

	if argv[len(argv)-1] != "/usr/libexec/muc/upd" {
		t.Errorf("argv = %v, want the update command last", argv)
	}

	if _, err := os.Stat("/run/systemd/system"); err != nil || os.Geteuid() != 0 {
		if len(argv) != 1 {
			t.Errorf("argv = %v, want a bare exec when systemd is absent or the client is unprivileged", argv)
		}
		return
	}
	if len(argv) == 1 {
		t.Skip("systemd is running but systemd-run is not installed")
	}
	// --pipe is deliberately absent. It passes our own file descriptors over
	// D-Bus, and on an SELinux system that message is refused for a service in
	// unconfined_service_t: the bus drops the connection and systemd-run reports
	// "Connection reset by peer" without running anything. The named unit and
	// the journal replace it.
	if containsArg(argv, "--pipe") {
		t.Error("argv passes --pipe; its fd passing is refused under SELinux and the run never starts")
	}
	for _, want := range []string{"--wait", "--collect", "--unit=muc-update-test"} {
		if !containsArg(argv, want) {
			t.Errorf("argv = %v, missing %s", argv, want)
		}
	}
}

func containsArg(argv []string, want string) bool {
	for _, arg := range argv {
		if arg == want {
			return true
		}
	}
	return false
}

// collectChunks records what the streamer publishes, in the order it published
// it, the way the server would receive it.
type chunkSink struct {
	mu     sync.Mutex
	chunks []string
	seqs   []int
}

func (c *chunkSink) publish(chunk string, seq int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chunks = append(c.chunks, chunk)
	c.seqs = append(c.seqs, seq)
}

func (c *chunkSink) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.chunks, "")
}

// TestOutputStreamerSendsEverythingInOrder is the guarantee the dashboard rests
// on: the chunks, concatenated in sequence order, are exactly what the command
// printed. A gap or a reordering would show as garbled output that looks like
// the update itself went wrong.
func TestOutputStreamerSendsEverythingInOrder(t *testing.T) {
	sink := &chunkSink{}
	s := newOutputStreamer(sink.publish)

	want := ""
	for i := 0; i < 50; i++ {
		line := "upd: upgrading package-" + strconv.Itoa(i) + "\n"
		want += line
		if _, err := s.Write([]byte(line)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	tail := s.close()

	if got := sink.joined(); got != want {
		t.Errorf("streamed %q, want %q", got, want)
	}
	for i, seq := range sink.seqs {
		if seq != i+1 {
			t.Errorf("chunk %d has seq %d; sequence numbers must count from 1 with no gaps", i, seq)
		}
	}
	if tail != want {
		t.Errorf("tail = %q, want the whole output (it is under the tail limit)", tail)
	}
}

// TestOutputStreamerFlushesEarlyOnVolume covers the burst case: a big write must
// not sit in the buffer waiting for the next tick.
func TestOutputStreamerFlushesEarlyOnVolume(t *testing.T) {
	sink := &chunkSink{}
	s := newOutputStreamer(sink.publish)

	s.Write([]byte(strings.Repeat("x", outputChunkLimit+1)))

	sink.mu.Lock()
	sent := len(sink.chunks)
	sink.mu.Unlock()
	if sent == 0 {
		t.Error("nothing was published for a write past the chunk limit; it waited for the flush tick")
	}
	s.close()
}

// TestOutputStreamerStopsStreamingPastTheLimit pins the safety valve, and that
// tripping it does not cost the run record: the tail still comes back.
func TestOutputStreamerStopsStreamingPastTheLimit(t *testing.T) {
	sink := &chunkSink{}
	s := newOutputStreamer(sink.publish)

	for written := 0; written < outputStreamLimit+outputChunkLimit; written += outputChunkLimit {
		s.Write([]byte(strings.Repeat("x", outputChunkLimit)))
	}
	s.Write([]byte("the last line\n"))
	tail := s.close()

	streamed := sink.joined()
	if len(streamed) > outputStreamLimit+outputChunkLimit*2 {
		t.Errorf("streamed %d bytes, want streaming to stop near %d", len(streamed), outputStreamLimit)
	}
	if !strings.Contains(streamed, "live output stopped") {
		t.Error("streaming stopped without telling the dashboard why")
	}
	if !strings.HasSuffix(tail, "the last line\n") {
		t.Error("the run record lost the end of the output when streaming was capped")
	}
}

// TestOutputStreamerTailIsTheEnd checks the two jobs stay separate: streaming is
// capped by volume, the record is capped by keeping the tail.
func TestOutputStreamerTailIsTheEnd(t *testing.T) {
	s := newOutputStreamer(func(string, int) {})

	s.Write([]byte(strings.Repeat("a", updateOutputLimit)))
	s.Write([]byte("the end\n"))
	tail := s.close()

	if len(tail) > updateOutputLimit+len("[earlier output truncated]\n") {
		t.Errorf("tail is %d bytes, want about %d", len(tail), updateOutputLimit)
	}
	if !strings.HasSuffix(tail, "the end\n") {
		t.Error("the tail dropped the most recent output instead of the oldest")
	}
}

// TestFallbackNoteExplainsItself is about the message an operator reads at 11pm
// when an update did not happen. It has to name the cause, say what is being
// done instead, and — when the sandbox will defeat the fallback too — say so
// before they spend an hour on it.
func TestFallbackNoteExplainsItself(t *testing.T) {
	note := fallbackNote("Failed to start transient service unit: Connection reset by peer", false)

	if !strings.Contains(note, "Connection reset by peer") {
		t.Errorf("note = %q, want it to carry systemd's own reason", note)
	}
	if !strings.Contains(note, "directly") {
		t.Errorf("note = %q, want it to say what is happening instead", note)
	}
	if strings.Contains(note, "ProtectSystem") {
		t.Errorf("note = %q, want no sandbox advice when /usr is writable", note)
	}

	sandboxed := fallbackNote("Connection reset by peer", true)
	if !strings.Contains(sandboxed, "ProtectSystem") {
		t.Errorf("note = %q, want the read-only /usr warning and how to lift it", sandboxed)
	}
}

// TestUsrIsReadOnlyMatchesTheMountTable pins the check against reality on the
// host running the tests: /usr is writable outside the client's sandbox, and a
// false positive here would print sandbox advice to operators who have no
// sandbox problem.
func TestUsrIsReadOnlyMatchesTheMountTable(t *testing.T) {
	if _, err := os.Stat("/proc/self/mounts"); err != nil {
		t.Skip("no /proc/self/mounts on this platform")
	}

	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		t.Fatal(err)
	}
	want := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "/usr" {
			for _, opt := range strings.Split(fields[3], ",") {
				if opt == "ro" {
					want = true
				}
			}
		}
	}

	if got := usrIsReadOnly(); got != want {
		t.Errorf("usrIsReadOnly() = %v, want %v for this host's mount table", got, want)
	}
}

// TestProbeSystemdRun covers the probe itself against a stand-in for
// systemd-run: it must pass silently when a transient unit can be started, and
// carry systemd's own words back when it cannot.
func TestProbeSystemdRun(t *testing.T) {
	dir := t.TempDir()

	working := filepath.Join(dir, "systemd-run-ok")
	if err := os.WriteFile(working, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := probeSystemdRun(working); err != nil {
		t.Errorf("probeSystemdRun on a working systemd-run returned %v, want nil", err)
	}

	broken := filepath.Join(dir, "systemd-run-broken")
	script := "#!/bin/sh\necho 'Failed to start transient service unit: Connection reset by peer' >&2\nexit 1\n"
	if err := os.WriteFile(broken, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	err := probeSystemdRun(broken)
	if err == nil {
		t.Fatal("probeSystemdRun on a broken systemd-run returned nil; the run would fail with no explanation")
	}
	if !strings.Contains(err.Error(), "Connection reset by peer") {
		t.Errorf("error = %q, want systemd's own message", err)
	}
}
