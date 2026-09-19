package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Remote updates let the dashboard ask this host to install its pending
// packages. The client is the side that opts in: without allow_remote_updates
// in the client config it never subscribes to the command subject, so the
// request has no responder and the server can only report that the host is not
// listening. That keeps a default install unable to patch anything, whatever
// the server is configured to offer.
//
// This is an opt-in convenience for a trusted network, not an authorization
// boundary: NATS itself is unauthenticated here, so anyone who can reach the
// NATS port can publish the same command. Enable it where that is acceptable.
const (
	// updateCommandSubjectPrefix carries the request, addressed to one host.
	// It deliberately sits outside "systems.updates.>", which is the check-in
	// stream the server subscribes to.
	updateCommandSubjectPrefix = "systems.commands.update."
	// updateResultSubjectPrefix carries progress back: one message when the run
	// starts and one when it ends.
	updateResultSubjectPrefix = "systems.results.update."
	// updateOutputSubjectPrefix carries the command's output while it runs, so
	// the dashboard can show a long upgrade proceeding instead of a spinner.
	updateOutputSubjectPrefix = "systems.output.update."

	// updateRunTimeout bounds a single run. A large dist-upgrade over a slow
	// link is legitimately slow, so this is generous; it exists to stop a run
	// that is wedged on a lock or a prompt from occupying the host forever.
	updateRunTimeout = 30 * time.Minute

	// updateOutputLimit is how much of the command's output is kept in the run
	// record, which is what survives a page reload. The tail is the useful part
	// — the error and the summary land there — and it keeps a 2000-package
	// upgrade from filling a NATS message.
	updateOutputLimit = 8 * 1024

	// Live output is coalesced rather than published line by line: a package
	// manager emits hundreds of short lines, and one message each would be a
	// message storm for no visible gain at human reading speed.
	outputFlushInterval = 400 * time.Millisecond
	// A chunk is flushed early once it reaches this size, well under the 1 MB
	// NATS payload limit.
	outputChunkLimit = 16 * 1024
	// updateUnitPrefix names the transient unit a run gets, so its output can be
	// followed in the journal and found there afterwards.
	updateUnitPrefix = "muc-update-"
	// journalSettleDelay is how long to keep following the journal after the
	// unit has exited, so the last lines it wrote are not cut off.
	journalSettleDelay = 2 * time.Second

	// Total live output per run. A pathological run (a repo serving a binary
	// blob to stderr, say) stops streaming at this point; the run still finishes
	// and its tail still arrives in the final record.
	outputStreamLimit = 2 * 1024 * 1024

	updateStatusRunning   = "running"
	updateStatusSucceeded = "succeeded"
	updateStatusFailed    = "failed"
)

// updateCommandCandidates are searched in order when update_command is unset.
// The packaged location comes first; /usr/local/bin/upd is where the script is
// installed by hand, which is how it got onto hosts before it was packaged.
var updateCommandCandidates = []string{
	"/usr/libexec/muc/upd",
	"/usr/local/bin/upd",
	"/usr/bin/upd",
}

// updateRequest is what the server publishes to ask for an update run.
type updateRequest struct {
	ID          string `json:"id"`
	RequestedAt string `json:"requested_at"`
	RequestedBy string `json:"requested_by,omitempty"`
}

// updateAck is the immediate reply: whether this host took the job. It answers
// the request synchronously so the dashboard can say "started" or "refused"
// rather than leaving the operator watching an unchanged page.
type updateAck struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Command  string `json:"command,omitempty"`
}

// updateOutput is one coalesced piece of the command's output, published as the
// run proceeds. Its JSON must stay in step with the server's runlog.Chunk.
//
// Seq numbers the chunks of one run so the dashboard can say that something was
// dropped rather than silently showing output with a hole in it.
type updateOutput struct {
	ID    string `json:"id"`
	Seq   int    `json:"seq"`
	Chunk string `json:"chunk"`
}

// updateRun is the progress record, published twice per run. Its JSON must stay
// in step with models.UpdateRun on the server.
type updateRun struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	RequestedBy string `json:"requested_by,omitempty"`
	Command     string `json:"command,omitempty"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
	ExitCode    int    `json:"exit_code"`
	Error       string `json:"error,omitempty"`
	Output      string `json:"output,omitempty"`
}

// updateRunner owns the one-at-a-time execution of the update command.
type updateRunner struct {
	nc       *nats.Conn
	hostname string
	command  string
	// recheck asks the main loop to re-read the package manager once the run
	// settles, so the dashboard reflects the result without waiting out the
	// poll interval. The path unit that watches the package database does the
	// same thing, but it is not installed everywhere.
	recheck chan<- struct{}

	mu      sync.Mutex
	running bool
}

// startUpdateListener subscribes to this host's update-command subject. It
// returns an error when remote updates cannot be served, so the caller can
// leave the capability unadvertised rather than offer the dashboard a button
// that will always fail. The runner comes back so the reboot listener can ask
// whether a run is in flight.
func startUpdateListener(nc *nats.Conn, hostname string, cfg ClientConfig, recheck chan<- struct{}) (*updateRunner, error) {
	command, err := resolveUpdateCommand(cfg.UpdateCommand)
	if err != nil {
		return nil, err
	}

	r := &updateRunner{nc: nc, hostname: hostname, command: command, recheck: recheck}

	subject := updateCommandSubjectPrefix + hostname
	if _, err := nc.Subscribe(subject, r.handle); err != nil {
		return nil, fmt.Errorf("subscribing to %s: %w", subject, err)
	}

	slog.Info("Remote updates enabled; listening for update commands",
		"subject", subject, "command", command)
	return r, nil
}

// resolveUpdateCommand picks the update command. A configured path must exist
// and be executable — falling back silently would run something other than what
// the administrator asked for.
func resolveUpdateCommand(configured string) (string, error) {
	if configured != "" {
		if err := checkExecutable(configured); err != nil {
			return "", fmt.Errorf("update_command %q is unusable: %w", configured, err)
		}
		return configured, nil
	}

	for _, candidate := range updateCommandCandidates {
		if err := checkExecutable(candidate); err == nil {
			return candidate, nil
		}
	}

	if path, err := exec.LookPath("upd"); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("no update command found (looked for %v and 'upd' on PATH); set update_command to point at one",
		updateCommandCandidates)
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("is a directory")
	}
	if info.Mode()&0111 == 0 {
		return errors.New("is not executable")
	}
	return nil
}

func (r *updateRunner) handle(m *nats.Msg) {
	var req updateRequest
	if len(m.Data) > 0 {
		if err := json.Unmarshal(m.Data, &req); err != nil {
			slog.Error("Ignoring malformed update command", "error", err)
			r.reply(m, updateAck{Hostname: r.hostname, Reason: "malformed update command: " + err.Error()})
			return
		}
	}
	if req.ID == "" {
		req.ID = newRunID()
	}

	if !r.claim() {
		slog.Warn("Refusing update command; a run is already in progress",
			"id", req.ID, "requested_by", req.RequestedBy)
		r.reply(m, updateAck{ID: req.ID, Hostname: r.hostname, Reason: "an update is already running on this host"})
		return
	}

	slog.Info("Accepted remote update command",
		"id", req.ID, "requested_by", req.RequestedBy, "command", r.command)
	r.reply(m, updateAck{ID: req.ID, Hostname: r.hostname, Accepted: true, Command: r.command})

	go r.run(req)
}

// claim takes the single run slot, reporting whether it was free.
func (r *updateRunner) claim() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return false
	}
	r.running = true
	return true
}

func (r *updateRunner) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = false
}

// isRunning reports whether a run holds the slot. nil-safe, so a caller that
// has no runner (remote updates off) can hold a nil and ask anyway.
func (r *updateRunner) isRunning() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *updateRunner) run(req updateRequest) {
	defer r.release()

	started := time.Now().UTC()
	run := updateRun{
		ID:          req.ID,
		Status:      updateStatusRunning,
		RequestedBy: req.RequestedBy,
		Command:     r.command,
		StartedAt:   started.Format(time.RFC3339),
	}
	r.publish(run)

	ctx, cancel := context.WithTimeout(context.Background(), updateRunTimeout)
	defer cancel()

	// Prefer the transient unit, but never let the wrapper be the reason an
	// update does not happen: if systemd will not start one, say so in the
	// output and run the command directly.
	unit := unitName(req.ID)
	argv := updateArgv(r.command, unit)
	wrapped := len(argv) > 1
	var note string
	if wrapped {
		if probeErr := probeSystemdRun(argv[0]); probeErr != nil {
			slog.Warn("Cannot start a transient systemd unit; running the update directly",
				"error", probeErr, "command", r.command)
			note = fallbackNote(probeErr.Error(), usrIsReadOnly())
			argv = []string{r.command}
			wrapped = false
		}
	}

	out := newOutputStreamer(r.outputPublisher(req.ID))
	if note != "" {
		out.Write([]byte(note))
	}

	// The transient unit logs to the journal, so that is where its output is
	// read from, live. A direct run writes to the pipes below instead.
	stopFollowing := func() {}
	if wrapped {
		stopFollowing = followUnitJournal(unit, started.Add(-time.Second), out)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// The same writer for both streams: os/exec notices they are identical and
	// gives the child one pipe, so the interleaving matches what a terminal
	// would show and nothing writes to the buffer concurrently.
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Env = updateEnv()

	err := cmd.Run()

	// journald has the unit's last lines a moment after the unit exits; cutting
	// the follower off at once would lose the summary a run ends with.
	if wrapped {
		time.Sleep(journalSettleDelay)
		stopFollowing()
	}

	run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	// Stops the flusher and sends whatever is left, so the last lines of a run
	// reach the dashboard before the record that says it ended.
	run.Output = out.close()

	// A unit that never got as far as running the command logs nothing of its
	// own; systemd's commentary on the unit is then the only explanation there
	// is, so fall back to it rather than reporting a failure with no detail.
	if wrapped && strings.TrimSpace(run.Output) == "" {
		if fromJournal := unitJournal(unit, started.Add(-time.Second)); fromJournal != "" {
			run.Output = fromJournal
		}
	}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		run.Status = updateStatusFailed
		run.ExitCode = -1
		run.Error = "timed out after " + updateRunTimeout.String()
	case err == nil:
		run.Status = updateStatusSucceeded
	default:
		run.Status = updateStatusFailed
		run.Error = err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			run.ExitCode = exitErr.ExitCode()
			run.Error = "update command exited with status " + strconv.Itoa(exitErr.ExitCode())
		} else {
			run.ExitCode = -1
		}
	}

	if run.Status == updateStatusSucceeded {
		slog.Info("Remote update finished", "id", run.ID, "duration", time.Since(started).Round(time.Second))
	} else {
		slog.Error("Remote update failed", "id", run.ID, "error", run.Error,
			"duration", time.Since(started).Round(time.Second))
	}
	r.publish(run)

	// Whatever the outcome, what is pending has probably changed.
	select {
	case r.recheck <- struct{}{}:
	default:
	}
}

// outputPublisher returns the sink the streamer flushes into. Output is fire
// and forget: a chunk that does not make it is a gap in a live view, not a lost
// result — the run record carries the tail regardless.
func (r *updateRunner) outputPublisher(id string) func(string, int) {
	subject := updateOutputSubjectPrefix + r.hostname
	return func(chunk string, seq int) {
		data, err := json.Marshal(updateOutput{ID: id, Seq: seq, Chunk: chunk})
		if err != nil {
			slog.Error("Failed to marshal update output", "error", err)
			return
		}
		if err := r.nc.Publish(subject, data); err != nil {
			slog.Debug("Failed to publish update output", "subject", subject, "error", err)
		}
	}
}

func (r *updateRunner) publish(run updateRun) {
	data, err := json.Marshal(run)
	if err != nil {
		slog.Error("Failed to marshal update run", "error", err)
		return
	}
	subject := updateResultSubjectPrefix + r.hostname
	if err := r.nc.Publish(subject, data); err != nil {
		slog.Error("Failed to publish update run status", "subject", subject, "error", err)
		return
	}
	if err := r.nc.Flush(); err != nil {
		slog.Warn("Failed to flush update run status", "error", err)
	}
}

func (r *updateRunner) reply(m *nats.Msg, ack updateAck) {
	if m.Reply == "" {
		return
	}
	data, err := json.Marshal(ack)
	if err != nil {
		slog.Error("Failed to marshal update ack", "error", err)
		return
	}
	if err := m.Respond(data); err != nil {
		slog.Error("Failed to reply to update command", "error", err)
	}
}

// updateArgv wraps the update command in a transient systemd unit where systemd
// is running. Two reasons, both of which break a naive fork from the daemon:
//
//   - muc-client.service is sandboxed with ProtectSystem=full, which makes /usr
//     read-only. A package manager inheriting that cannot install anything.
//   - the transaction may upgrade muc-client itself, and restarting the unit
//     kills everything in its cgroup — including a dnf halfway through writing
//     the rpm database. A transient unit has its own cgroup and survives.
//
// The unit is named rather than anonymous, and its output goes to the journal,
// which is where this reads it back from. The obvious alternative — systemd-run
// --pipe, which hands our own stdout to the unit — passes those file
// descriptors over D-Bus, and on an SELinux system that message is refused for
// a service in unconfined_service_t: the bus drops the connection and
// systemd-run reports "Failed to start transient service unit: Connection reset
// by peer" without ever running anything. Rocky 10 does exactly this. Going
// through the journal asks nothing of the bus beyond starting the unit, works in
// that domain, and leaves the full log on the host as a side effect.
func updateArgv(command, unit string) []string {
	// Only root can talk to the system manager; an unprivileged client would
	// just get a permission error where a plain exec still works (the script
	// re-execs itself through sudo).
	if os.Geteuid() != 0 {
		return []string{command}
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return []string{command}
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return []string{command}
	}
	return []string{
		systemdRun,
		"--quiet",
		"--unit=" + unit,
		"--wait",
		"--collect",
		"--description=muc remote package update",
		"--setenv=LC_ALL=C",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"--property=RuntimeMaxSec=" + strconv.Itoa(int(updateRunTimeout.Seconds())),
		// An upgrade of any size will trip journald's default rate limit, and a
		// silently thinned-out log is worse than a verbose one.
		"--property=LogRateLimitIntervalSec=0",
		command,
	}
}

// unitName is the transient unit for one run.
func unitName(id string) string {
	return updateUnitPrefix + id
}

// followUnitJournal streams the transient unit's output into w as it is
// written. It returns a stop function, which is safe to call whether or not the
// follower actually started — journalctl may be absent, in which case the run
// still works and only the live view is lost.
func followUnitJournal(unit string, since time.Time, w io.Writer) func() {
	journalctl, err := exec.LookPath("journalctl")
	if err != nil {
		slog.Warn("journalctl not found; the update will run without live output", "error", err)
		return func() {}
	}

	// Match on _SYSTEMD_UNIT rather than -u: -u would also carry systemd's own
	// commentary about the unit, which is not the command's output.
	cmd := exec.Command(journalctl,
		"--no-pager",
		"--output=cat",
		"--follow",
		"--since=@"+strconv.FormatInt(since.Unix(), 10),
		"_SYSTEMD_UNIT="+unit+".service")
	cmd.Stdout = w
	cmd.Env = updateEnv()

	if err := cmd.Start(); err != nil {
		slog.Warn("Could not follow the update journal; the run continues without live output", "error", err)
		return func() {}
	}

	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}
}

// unitJournal reads back everything the unit logged, systemd's own lines
// included. It is the fallback for a run that produced no output of its own —
// a unit that failed to execute at all says so only in those lines.
func unitJournal(unit string, since time.Time) string {
	journalctl, err := exec.LookPath("journalctl")
	if err != nil {
		return ""
	}
	cmd := exec.Command(journalctl,
		"--no-pager",
		"--output=cat",
		"--since=@"+strconv.FormatInt(since.Unix(), 10),
		"--unit="+unit+".service")
	cmd.Env = updateEnv()

	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// probeSystemdRun checks that a transient unit can actually be started, by
// starting a trivial one. Asking is the only reliable way to tell: the failure
// otherwise arrives as the update command's own exit status, indistinguishable
// from an upgrade that genuinely failed.
func probeSystemdRun(systemdRun string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, systemdRun,
		"--quiet", "--wait", "--collect",
		"--description=muc transient unit probe",
		"/bin/true")
	cmd.Env = updateEnv()

	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if reason := strings.TrimSpace(string(out)); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	return err
}

// fallbackNote explains, in the output pane where the operator is already
// looking, why the run is not isolated and what that costs.
func fallbackNote(reason string, readOnlyUsr bool) string {
	note := "[muc] could not start a transient systemd unit: " + reason + "\n" +
		"[muc] running the update directly instead — it shares muc-client's cgroup,\n" +
		"[muc] so a transaction that restarts muc-client will kill it partway through.\n"
	if readOnlyUsr {
		note += "[muc] /usr is read-only for muc-client (ProtectSystem=), so installing packages\n" +
			"[muc] will fail here. Either fix transient units, or relax the sandbox:\n" +
			"[muc]   systemctl edit muc-client   →   [Service] / ProtectSystem=false\n"
	}
	return note + "\n"
}

// usrIsReadOnly reports whether /usr is read-only in this process's mount
// namespace, which is what ProtectSystem= does to the client. It is the
// difference between a fallback that works and one that fails on the first
// package written.
func usrIsReadOnly() bool {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != "/usr" {
			continue
		}
		for _, opt := range strings.Split(fields[3], ",") {
			if opt == "ro" {
				return true
			}
		}
	}
	return false
}

// updateEnv gives the command a predictable environment rather than whatever
// the daemon inherited: a full PATH (package managers live in sbin on older
// distributions), C messages so the captured output is parseable by a human
// reading English, and no interactive debconf prompts.
func updateEnv() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"LC_ALL=C",
		"DEBIAN_FRONTEND=noninteractive",
	}
}

// tailWriter keeps only the last limit bytes written to it. A full upgrade log
// is megabytes; the end of it is what says whether the run worked.
type tailWriter struct {
	limit     int
	buf       []byte
	truncated bool
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.limit {
		w.truncated = true
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.limit:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	if w.truncated {
		return "[earlier output truncated]\n" + string(w.buf)
	}
	return string(w.buf)
}

// outputStreamer is what the update command writes to. It does two jobs at
// once: it keeps the tail for the run record, and it flushes what has arrived
// since the last flush to the dashboard as a live chunk.
type outputStreamer struct {
	publish func(chunk string, seq int)

	mu       sync.Mutex
	tail     tailWriter
	pending  []byte
	seq      int
	streamed int
	capped   bool

	done chan struct{}
	wg   sync.WaitGroup
}

func newOutputStreamer(publish func(chunk string, seq int)) *outputStreamer {
	s := &outputStreamer{
		publish: publish,
		tail:    tailWriter{limit: updateOutputLimit},
		done:    make(chan struct{}),
	}
	s.wg.Add(1)
	go s.flushLoop()
	return s
}

// flushLoop keeps output moving during the long quiet stretches of an upgrade,
// where a chunk would otherwise sit in the buffer until the next burst.
func (s *outputStreamer) flushLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(outputFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flush()
		case <-s.done:
			return
		}
	}
}

func (s *outputStreamer) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.tail.Write(p)
	if !s.capped {
		s.pending = append(s.pending, p...)
	}
	full := len(s.pending) >= outputChunkLimit
	s.mu.Unlock()

	if full {
		s.flush()
	}
	return len(p), nil
}

// flush publishes what has accumulated. It holds the lock across the publish so
// chunks cannot be numbered in one order and sent in another.
func (s *outputStreamer) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.pending) == 0 {
		return
	}
	chunk := string(s.pending)
	s.pending = s.pending[:0]
	s.streamed += len(chunk)
	if s.streamed >= outputStreamLimit && !s.capped {
		s.capped = true
		chunk += "\n[live output stopped here; the end of the run is shown when it finishes]\n"
	}
	s.seq++
	s.publish(chunk, s.seq)
}

// close stops streaming, sends the remainder, and returns the tail for the run
// record. It must be called exactly once.
func (s *outputStreamer) close() string {
	close(s.done)
	s.wg.Wait()
	s.flush()

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tail.String()
}

// newRunID labels a run so the dashboard can tell one from the next. The server
// normally supplies it; this covers a request that arrived without one.
func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}
