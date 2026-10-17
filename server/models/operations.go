package models

// Operation is one line of the operations log: something the dashboard asked a
// host to do, or something a host reported back about it.
//
// A request and the results that follow it share an ID, so one update can be
// read as a story — requested, running, succeeded — across several lines. The
// log is append-only; nothing here is ever rewritten, which is what makes it
// worth keeping alongside the LastUpdateRun and LastReboot records, which are.
type Operation struct {
	// Seq orders the log and is assigned by storage. It is also the cursor
	// for reading further back.
	Seq  uint64 `json:"seq"`
	Time string `json:"time"`
	// Hostname is the host the operation was about. It is kept after the host
	// is removed: who rebooted a machine is still worth knowing once it is
	// gone.
	Hostname string `json:"hostname"`
	// Action is update, reboot, or checkin.
	Action string `json:"action"`
	// Event is what happened — see the Operation* constants.
	Event string `json:"event"`
	// Outcome is set on a requested event only: accepted, skipped, refused,
	// unreachable, or failed, as the dashboard reported it.
	Outcome     string `json:"outcome,omitempty"`
	ID          string `json:"id,omitempty"`
	RequestedBy string `json:"requested_by,omitempty"`
	// Group is the group the request was made through, where it was.
	Group   string `json:"group,omitempty"`
	Command string `json:"command,omitempty"`
	// Detail is the reason, error, or exit status, in a sentence.
	Detail string `json:"detail,omitempty"`
}

// Operation events. Requested is the server's own record of asking; the rest
// are what the host reported, or — for rebooted — what its next check-in
// showed.
const (
	OperationRequested = "requested"
	OperationRunning   = "running"
	OperationSucceeded = "succeeded"
	OperationFailed    = "failed"
	OperationRebooting = "rebooting"
	OperationRebooted  = "rebooted"
)
