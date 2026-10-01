package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"server/models"
	"server/storage"
	"slices"
	"strings"

	"github.com/gorilla/mux"
)

// GroupActionResponse is what a group action reports back.
//
// The status is 200 whenever the group exists and the feature is on, even when
// some members were skipped or could not be reached. The request was "fan this
// out and tell me what happened", and it did exactly that — nothing about the
// HTTP transaction failed. A status cannot summarise seven different answers,
// so it does not try: the body is the thing to read. (207 Multi-Status would be
// no better. It is a WebDAV code whose body is a defined XML document, no
// client treats it specially, and fetch's response.ok is already true for it.)
type GroupActionResponse struct {
	Group     string             `json:"group"`
	Action    string             `json:"action"`
	Requested int                `json:"requested"`
	Accepted  int                `json:"accepted"`
	Skipped   int                `json:"skipped"`
	Failed    int                `json:"failed"`
	Results   []HostActionResult `json:"results"`
}

// groupWriteRequest is the body of PUT /api/groups/{group}.
//
// Both fields are pointers so that leaving one out and sending it empty are
// different requests: omitting members means "rename only, leave membership
// alone", while "members": [] means "empty this group".
type groupWriteRequest struct {
	Name    *string   `json:"name"`
	Members *[]string `json:"members"`
}

// groupCreateRequest is the body of POST /api/groups.
type groupCreateRequest struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// hostGroupsRequest is the body of PUT /api/systems/{hostname}/groups: the
// complete set of groups the host should now be in.
type hostGroupsRequest struct {
	Groups []string `json:"groups"`
}

// isNotFound reports whether a storage error means "no such thing". The bbolt
// layer spells it in its message, as DeleteSystemHandler has always relied on.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

// resolveGroup finds a group by name, stored or derived.
//
// Stored groups are checked first and the systems are only loaded when the
// name is in the derived namespace, so an ordinary group lookup stays a single
// key read.
func resolveGroup(store storage.Storage, name string) (models.Group, error) {
	if !models.IsDerivedName(name) {
		return store.GetGroup(name)
	}

	systems, err := store.GetAllSystems()
	if err != nil {
		return models.Group{}, err
	}
	group, ok := models.FindDerivedGroup(systems, name)
	if !ok {
		// A derived group with no members does not exist, which is why this
		// reads as "not found" rather than as an empty group: nothing is
		// Fedora, so there is no os:fedora to act on.
		return models.Group{}, fmt.Errorf("group '%s' not found", name)
	}
	return group, nil
}

// refuseIfDerived writes the explanation and reports true when a route that
// changes a group has been pointed at one that is computed.
func refuseIfDerived(w http.ResponseWriter, name string) bool {
	if !models.IsDerivedName(name) {
		return false
	}
	writeAPIError(w, http.StatusConflict,
		name+" is a derived group: its members come from what the hosts report, so it cannot be edited. "+
			"Change the host, or make an ordinary group instead.")
	return true
}

// ListGroupsHandler returns every group. The dashboard asks for this on load
// and after every edit, and inverts it into a per-host view itself — which is
// why groups are not folded into the /api/systems payload.
func ListGroupsHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groups, err := store.GetAllGroups()
		if err != nil {
			slog.Error("Failed to list groups", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "Failed to fetch groups")
			return
		}
		if groups == nil {
			groups = []models.Group{}
		}

		// Derived groups are computed on every read rather than stored, so
		// they are always current and there is never a stale one to clean up.
		// A failure here is not fatal: the hand-made groups are still worth
		// returning.
		if systems, err := store.GetAllSystems(); err != nil {
			slog.Error("Failed to read systems for derived groups", "error", err)
		} else {
			groups = append(groups, models.DeriveGroups(systems)...)
		}

		writeJSON(w, http.StatusOK, groups)
	}
}

// GetGroupHandler returns one group, matched without regard to case.
func GetGroupHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		group, err := resolveGroup(store, mux.Vars(r)["group"])
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "Group not found")
			return
		}
		writeJSON(w, http.StatusOK, group)
	}
}

// CreateGroupHandler makes a new, usually empty, group.
//
// Creating a group is deliberately its own act rather than something that
// happens the first time a name is typed into the host editor: a typo that
// founds a group of one is far harder to notice than a typo that is refused.
func CreateGroupHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req groupCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		name := models.NormalizeGroupName(req.Name)
		if err := models.ValidateGroupName(name); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}

		if existing, err := store.GetGroup(name); err == nil {
			writeAPIError(w, http.StatusConflict,
				"A group called "+existing.Name+" already exists")
			return
		}

		group := models.Group{Name: name, Members: req.Members}
		if err := store.SaveGroup(group); err != nil {
			slog.Error("Failed to create group", "group", name, "error", err)
			writeAPIError(w, http.StatusInternalServerError, "Failed to create group")
			return
		}

		slog.Info("Group created", "group", name, "members", len(req.Members), "requested_by", requesterAddress(r))

		saved, err := store.GetGroup(name)
		if err != nil {
			saved = group
		}
		writeJSON(w, http.StatusCreated, saved)
	}
}

// UpdateGroupHandler renames a group, replaces its membership, or both.
func UpdateGroupHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.Vars(r)["group"]
		if refuseIfDerived(w, name) {
			return
		}

		var req groupWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		group, err := store.GetGroup(name)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "Group not found")
			return
		}

		if req.Members != nil {
			group.Members = *req.Members
			if err := store.SaveGroup(group); err != nil {
				slog.Error("Failed to set group members", "group", group.Name, "error", err)
				writeAPIError(w, http.StatusInternalServerError, "Failed to update group")
				return
			}
		}

		if req.Name != nil {
			newName := models.NormalizeGroupName(*req.Name)
			if err := models.ValidateGroupName(newName); err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
			// A change of capitalisation alone is a relabel in place rather
			// than a move, and the storage layer already tells the two apart.
			if newName != group.Name {
				switch err := store.RenameGroup(group.Name, newName); {
				case err == nil:
				case isNotFound(err):
					writeAPIError(w, http.StatusNotFound, "Group not found")
					return
				case strings.Contains(err.Error(), "already exists"):
					writeAPIError(w, http.StatusConflict,
						"A group called "+newName+" already exists")
					return
				default:
					slog.Error("Failed to rename group", "group", group.Name, "error", err)
					writeAPIError(w, http.StatusInternalServerError, "Failed to rename group")
					return
				}
				group.Name = newName
				slog.Info("Group renamed", "group", name, "to", newName, "requested_by", requesterAddress(r))
			}
		}

		saved, err := store.GetGroup(group.Name)
		if err != nil {
			saved = group
		}
		writeJSON(w, http.StatusOK, saved)
	}
}

// DeleteGroupHandler removes a group. The hosts in it are untouched — a group
// is a label, and dropping the label is not dropping the machines.
func DeleteGroupHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.Vars(r)["group"]
		if refuseIfDerived(w, name) {
			return
		}

		if err := store.DeleteGroup(name); err != nil {
			if isNotFound(err) {
				writeAPIError(w, http.StatusNotFound, "Group not found")
				return
			}
			slog.Error("Failed to delete group", "group", name, "error", err)
			writeAPIError(w, http.StatusInternalServerError, "Failed to delete group")
			return
		}

		slog.Info("Group deleted", "group", name, "requested_by", requesterAddress(r))
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "Group deleted successfully",
			"group":   name,
		})
	}
}

// RemoveGroupMemberHandler drops one host from one group.
//
// It is how a member the server no longer knows about gets evicted — a host
// that was decommissioned, or a name that was mistyped into the list. The
// per-host editor cannot do it, because that host has no row to expand.
func RemoveGroupMemberHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		hostname := strings.TrimSpace(vars["hostname"])
		if refuseIfDerived(w, vars["group"]) {
			return
		}

		group, err := store.GetGroup(vars["group"])
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "Group not found")
			return
		}

		if !slices.Contains(group.Members, hostname) {
			writeAPIError(w, http.StatusNotFound, hostname+" is not a member of "+group.Name)
			return
		}

		group.Members = slices.DeleteFunc(group.Members, func(m string) bool { return m == hostname })
		if err := store.SaveGroup(group); err != nil {
			slog.Error("Failed to remove group member", "group", group.Name, "hostname", hostname, "error", err)
			writeAPIError(w, http.StatusInternalServerError, "Failed to update group")
			return
		}

		saved, err := store.GetGroup(group.Name)
		if err != nil {
			saved = group
		}
		writeJSON(w, http.StatusOK, saved)
	}
}

// SetSystemGroupsHandler replaces one host's memberships with exactly the list
// it is given, so a group left out is a group the host leaves.
//
// The host need not have checked in: putting a machine into its groups before
// it is built is a reasonable thing to want, and membership that outlives the
// system record is already how groups behave.
func SetSystemGroupsHandler(store storage.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostname := strings.TrimSpace(mux.Vars(r)["hostname"])
		if hostname == "" {
			writeAPIError(w, http.StatusBadRequest, "Hostname is required")
			return
		}

		var req hostGroupsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		for _, name := range req.Groups {
			if models.IsDerivedName(name) {
				writeAPIError(w, http.StatusConflict,
					name+" is a derived group: a host joins it by being a Fedora box or needing a reboot, not by being put in it.")
				return
			}
		}

		if err := store.SetHostGroups(hostname, req.Groups); err != nil {
			if isNotFound(err) {
				// A name that is not already a group. Refusing is the point:
				// joining nothing is recoverable, quietly founding a group of
				// one is not.
				writeAPIError(w, http.StatusBadRequest,
					"No such group — create it before adding hosts to it ("+err.Error()+")")
				return
			}
			slog.Error("Failed to set host groups", "hostname", hostname, "error", err)
			writeAPIError(w, http.StatusInternalServerError, "Failed to update groups")
			return
		}

		groups, err := store.GetAllGroups()
		if err != nil {
			slog.Error("Failed to re-read groups", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "Failed to update groups")
			return
		}

		joined := []string{}
		for _, group := range groups {
			if slices.Contains(group.Members, hostname) {
				joined = append(joined, group.Name)
			}
		}

		slog.Info("Host groups set", "hostname", hostname, "groups", joined, "requested_by", requesterAddress(r))
		writeJSON(w, http.StatusOK, map[string]any{
			"hostname": hostname,
			"groups":   joined,
		})
	}
}

// GroupUpdateHandler asks every member of a group to install its pending
// packages. GroupCheckInHandler and GroupRebootHandler are the same shape.
//
// Gated exactly like the single-host route: the server flag decides whether the
// route works at all, and each host's own opt-in decides whether it is a
// candidate. A member that has not opted in is skipped, not an error — one
// machine that declines should not stop the other eleven being patched.
func GroupUpdateHandler(store storage.Storage, updater UpdateRequester) http.HandlerFunc {
	return groupActionHandler(store, actionUpdate, func() (requesters, bool, int, string) {
		return requesters{update: updater}, updater != nil, http.StatusForbidden,
			"Remote updates are disabled on this server (set remote_updates: true to enable them)"
	})
}

// GroupRebootHandler reboots every member of a group that reports a pending
// reboot. Members with nothing pending are skipped, which is also what makes
// the button safe to press after a group update: it reboots what needs it.
func GroupRebootHandler(store storage.Storage, rebooter RebootRequester) http.HandlerFunc {
	return groupActionHandler(store, actionReboot, func() (requesters, bool, int, string) {
		return requesters{reboot: rebooter}, rebooter != nil, http.StatusForbidden,
			"Remote reboots are disabled on this server (set remote_reboot: true to enable them)"
	})
}

// GroupCheckInHandler asks every member of a group to publish a fresh check-in.
//
// Nothing configured gates it, as with the single-host route — a check-in
// installs nothing. Its unavailability is an infrastructure fact rather than a
// policy one, which is why it answers 503 where the other two answer 403.
func GroupCheckInHandler(store storage.Storage, checkins CheckInRequester) http.HandlerFunc {
	return groupActionHandler(store, actionCheckIn, func() (requesters, bool, int, string) {
		return requesters{checkIn: checkins}, checkins != nil, http.StatusServiceUnavailable,
			"Check-in requests are unavailable: the server has no NATS connection"
	})
}

// groupActionHandler is the body all three group actions share: check the
// feature is on, look the group up, fan out, count the answers.
func groupActionHandler(store storage.Storage, act action, gate func() (requesters, bool, int, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, enabled, status, message := gate()
		if !enabled {
			writeAPIError(w, status, message)
			return
		}

		// Resolved when the action runs, not when the page was drawn. For a
		// derived group that is the point: "reboot everything that needs a
		// reboot" should mean what is true now.
		group, err := resolveGroup(store, mux.Vars(r)["group"])
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "Group not found")
			return
		}

		results := dispatchGroup(store, act, req, group.Members, requesterAddress(r))

		resp := GroupActionResponse{
			Group:     group.Name,
			Action:    string(act),
			Requested: len(results),
			Results:   results,
		}
		for _, res := range results {
			switch res.Outcome {
			case OutcomeAccepted:
				resp.Accepted++
			case OutcomeSkipped:
				resp.Skipped++
			default:
				// refused, unreachable and failed all read as "failed" in the
				// summary line; the per-host list keeps them apart.
				resp.Failed++
			}
		}

		// A group action is the most consequential thing this server does, and
		// nothing authenticates the person who pressed the button. One line per
		// action means "who rebooted production" is one grep away.
		slog.Warn("Group action dispatched",
			"group", group.Name, "action", string(act), "requested_by", requesterAddress(r),
			"requested", resp.Requested, "accepted", resp.Accepted,
			"skipped", resp.Skipped, "failed", resp.Failed)

		writeJSON(w, http.StatusOK, resp)
	}
}
