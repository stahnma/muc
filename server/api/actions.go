package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"server/models"
	"server/storage"
	"sync"
)

// action names the three things the dashboard can ask of a host.
//
// It exists so the precondition ladder below is written once instead of three
// times, and — the real point — so a group action is guaranteed to skip exactly
// what the single-host route would have refused. Those two paths drifting apart
// is the failure nobody would notice: the dashboard would offer a button for
// one host and quietly do nothing for the same host inside a group.
type action string

const (
	actionUpdate  action = "update"
	actionCheckIn action = "checkin"
	actionReboot  action = "reboot"
)

// The outcome of asking one host to do one thing.
//
// There are five rather than three because "skipped" and "refused" answer
// different questions, and an operator reading a group report needs to tell
// them apart: skipped is what the server knew before it sent anything, refused
// is what the host said back.
const (
	OutcomeAccepted    = "accepted"
	OutcomeSkipped     = "skipped"
	OutcomeRefused     = "refused"
	OutcomeUnreachable = "unreachable"
	OutcomeFailed      = "failed"
)

// Machine-readable reasons, so the dashboard can style a result without
// matching on prose.
const (
	CodeUnknownHost     = "unknown_host"
	CodeNotOptedIn      = "not_opted_in"
	CodeNoRebootPending = "no_reboot_pending"
	CodeHostRefused     = "host_refused"
	CodeNotListening    = "not_listening"
	CodeRequestFailed   = "request_failed"
)

// HostActionResult is what one host did when it was asked. Every member of a
// group gets exactly one, whether or not any command was actually sent to it.
//
// Reason is the same sentence the single-host route puts in its error body, so
// the two paths explain themselves identically.
type HostActionResult struct {
	Hostname string `json:"hostname"`
	Outcome  string `json:"outcome"`
	Code     string `json:"code,omitempty"`
	Reason   string `json:"reason,omitempty"`
	ID       string `json:"id,omitempty"`
	Command  string `json:"command,omitempty"`
}

// requesters bundles the three narrow interfaces so one value reaches dispatch.
// Any of them may be nil, but the nil check belongs to the caller: a feature is
// off once per request, not once per member, and the two routes that can be off
// say so differently.
type requesters struct {
	update  UpdateRequester
	checkIn CheckInRequester
	reboot  RebootRequester
}

// statusFor maps an outcome back to the HTTP status the single-host route has
// always returned for it. Keeping the mapping in one place is what lets a test
// assert that the group path skips precisely what the single-host path refuses.
func statusFor(res HostActionResult) int {
	switch res.Outcome {
	case OutcomeAccepted:
		return http.StatusAccepted
	case OutcomeSkipped:
		if res.Code == CodeUnknownHost {
			return http.StatusNotFound
		}
		return http.StatusConflict
	case OutcomeRefused:
		return http.StatusConflict
	case OutcomeUnreachable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

// dispatch runs the whole ladder for one host and one action: look the host up,
// check whichever opt-ins the action needs, send the command, read the answer.
// It never writes to the response — the single-host routes turn its result into
// a status code, the group routes turn it into one line of a result list.
func dispatch(store storage.Storage, act action, req requesters, hostname, requestedBy string) HostActionResult {
	res := HostActionResult{Hostname: hostname}

	system, err := store.GetSystem(hostname)
	if err != nil {
		res.Outcome, res.Code, res.Reason = OutcomeSkipped, CodeUnknownHost, "System not found"
		return res
	}

	switch act {
	case actionUpdate:
		if !system.RemoteUpdatesEnabled {
			res.Outcome, res.Code = OutcomeSkipped, CodeNotOptedIn
			res.Reason = "This host has not opted into remote updates (set allow_remote_updates: true in its client config)"
			return res
		}
	case actionReboot:
		if !system.RemoteRebootEnabled {
			res.Outcome, res.Code = OutcomeSkipped, CodeNotOptedIn
			res.Reason = "This host has not opted into remote reboots (set allow_remote_reboot: true in its client config)"
			return res
		}
		// The dashboard only enables its button when a reboot is pending, and
		// the API should not be a way around that. The host checks again for
		// itself when the command arrives.
		if !system.RebootRequired {
			res.Outcome, res.Code = OutcomeSkipped, CodeNoRebootPending
			res.Reason = "This host did not report a pending reboot at its last check-in"
			return res
		}
	}

	var (
		accepted bool
		reason   string
	)
	switch act {
	case actionUpdate:
		ack, reqErr := req.update.RequestUpdate(hostname, requestedBy)
		err, accepted, reason, res.ID, res.Command = reqErr, ack.Accepted, ack.Reason, ack.ID, ack.Command
	case actionReboot:
		ack, reqErr := req.reboot.RequestReboot(hostname, requestedBy)
		err, accepted, reason, res.ID = reqErr, ack.Accepted, ack.Reason, ack.ID
	case actionCheckIn:
		ack, reqErr := req.checkIn.RequestCheckIn(hostname, requestedBy)
		err, accepted, reason, res.ID = reqErr, ack.Accepted, ack.Reason, ack.ID
	}

	switch {
	case errors.Is(err, models.ErrHostNotListening):
		// The stored opt-in said yes but nothing answered, so the host is down
		// or its client has since been reconfigured.
		res.Outcome, res.Code, res.Reason = OutcomeUnreachable, CodeNotListening, notListeningReason(act, hostname)
		res.ID, res.Command = "", ""
		return res
	case err != nil:
		slog.Error(string(act)+" request failed", "hostname", hostname, "error", err)
		res.Outcome, res.Code = OutcomeFailed, CodeRequestFailed
		res.Reason = failedPrefix(act) + ": " + err.Error()
		res.ID, res.Command = "", ""
		return res
	}

	if !accepted {
		if reason == "" {
			reason = refusalReason(act)
		}
		res.Outcome, res.Code, res.Reason = OutcomeRefused, CodeHostRefused, reason
		return res
	}

	res.Outcome = OutcomeAccepted
	return res
}

// notListeningReason, failedPrefix and refusalReason keep the per-action
// wording the single-host routes have always used. The sentences differ by
// action on purpose — "too old to accept check-in requests" is true of a client
// from before check-ins existed, and would be wrong about an update.
func notListeningReason(act action, hostname string) string {
	switch act {
	case actionUpdate:
		return "No response from " + hostname + ": it is offline, or its client is no longer accepting update commands"
	case actionReboot:
		return "No response from " + hostname + ": it is offline, or its client is no longer accepting reboot commands"
	default:
		return "No response from " + hostname + ": it is offline, or its client is too old to accept check-in requests"
	}
}

func failedPrefix(act action) string {
	switch act {
	case actionUpdate:
		return "Update request failed"
	case actionReboot:
		return "Reboot request failed"
	default:
		return "Check-in request failed"
	}
}

func refusalReason(act action) string {
	switch act {
	case actionUpdate:
		return "the host refused the update request"
	case actionReboot:
		return "the host refused the reboot request"
	default:
		return "the host refused the check-in request"
	}
}

// dispatchGroup fans one action out to every member at once and collects what
// each host said.
//
// Members are not paced. The point of a group is that it happens now; the fleet
// this serves is tens of hosts rather than thousands, serialising a group
// check-in would make it take minutes for no benefit, and the hosts enforce
// their own minimum gap between commands anyway.
//
// Results come back in the group's member order whatever order they finished
// in, so the list reads the same twice running. The whole call is bounded by
// one host's request timeout — ten seconds — rather than by the number of
// members, because they all wait at the same time. That is why there is no
// deadline here: one would only invent a failure mode.
//
// Nor is the request context threaded through. The three requester interfaces
// take no context and a published NATS request cannot be recalled, so
// cancelling the wait would end goroutines that were going to finish inside ten
// seconds anyway, while the commands it "cancelled" still ran.
func dispatchGroup(store storage.Storage, act action, req requesters, members []string, requestedBy string) []HostActionResult {
	results := make([]HostActionResult, len(members))

	var wg sync.WaitGroup
	for i, hostname := range members {
		wg.Add(1)
		go func(i int, hostname string) {
			defer wg.Done()
			// Each goroutine owns one index of a pre-sized slice, so there is
			// nothing to lock.
			results[i] = dispatch(store, act, req, hostname, requestedBy)
		}(i, hostname)
	}
	wg.Wait()

	return results
}

// writeJSON is the success-path counterpart to writeAPIError.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("Failed to write response", "error", err)
	}
}
