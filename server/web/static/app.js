document.addEventListener("DOMContentLoaded", () => {
    const systemsTable = document.getElementById("systems");
    if (!systemsTable) {
        console.error("[ERROR] Systems table element not found!");
        return;
    }
    let sortOrder = {
        column: "hostname", // Default sort column
        ascending: true, // Default sort order
    };
    let systemsData = []; // Store the current systems data - always initialize as empty array
    let ws = null;
    let pingIntervalId = null; // Store ping interval ID for cleanup
    let timestampUpdateIntervalId = null; // Store timestamp update interval ID for cleanup
    let reconnectAttempts = 0;
    const MAX_RECONNECT_ATTEMPTS = 10;
    const INITIAL_RECONNECT_DELAY = 1000; // 1 second
    let expandedSystems = new Set(); // Track manually expanded systems

    // Optional server capabilities, read from /api/features. Remote updates are
    // assumed off until the server says otherwise, so a server that does not
    // offer them never draws a button for them.
    let features = { remote_updates: false, remote_reboot: false };

    // Row-button labels. Short on purpose: these sit on every row of the
    // table, and the column is only as wide as its longest label.
    const RUN_UPDATE_LABEL = "Update";
    const RUN_UPDATE_STARTING_LABEL = "Starting\u2026";
    const RUN_UPDATE_RUNNING_LABEL = "Updating\u2026";
    const REBOOT_LABEL = "Reboot";
    const REBOOT_WAITING_LABEL = "Rebooting\u2026";
    const CHECK_IN_LABEL = "Check in";
    const CHECK_IN_WAITING_LABEL = "Checking\u2026";

    // Hosts whose update request is in flight: between the click and the run
    // record arriving over the WebSocket, a re-render would otherwise put the
    // button back to "Update" and invite a second click.
    const startingUpdates = new Set();

    // Check-ins asked for from here, keyed by hostname: {since, timer}. A
    // commanded check-in has no reply to wait for — the host says "will do" and
    // then publishes an ordinary check-in — so the button is held until that
    // payload arrives, and this is what remembers which hosts are waiting for
    // one across the re-renders in between.
    const pendingCheckIns = new Map();

    // How long to hold a check-in button before giving up on it. Generous: a
    // cold `dnf check-update --refresh` against a slow mirror is legitimately
    // this slow, and the cost of being wrong is only that the button comes back
    // early.
    const CHECK_IN_TIMEOUT_MS = 120000;

    // Hosts whose reboot checkbox is ticked. The table re-renders on every
    // check-in from any host, so the tick has to live here or an unrelated
    // host checking in would clear it under the operator's cursor. A host
    // leaves the set when its reboot is sent or its row is collapsed.
    const armedReboots = new Set();

    // Reboots asked for from here, keyed by hostname: {id, timer}. The host
    // says "going down" and then its record arrives over the WebSocket as an
    // ordinary system update; this holds the button until that record lands,
    // and gives up after a while if it never does.
    const pendingReboots = new Map();
    const REBOOT_PENDING_TIMEOUT_MS = 30000;

    // How long a host may be "rebooting" before the dashboard stops taking
    // that at face value. A host that has not checked in this long after a
    // reboot was requested has not come back, and the row should say so
    // rather than show a spinner forever.
    const REBOOT_OVERDUE_MS = 10 * 60 * 1000;

    // Live output of update runs, keyed by hostname: {id, seq, text}. It is
    // held here rather than re-read from the server because the table re-renders
    // on every check-in, and a pane rebuilt from scratch mid-run would flicker
    // back to whatever the last saved record said.
    const liveOutput = new Map();

    // Per-host state of the output pane: whether it is open, and where the reader
    // is in it. The table rebuilds itself on every check-in, so without this a
    // pane being read closes and jumps the moment anything else happens.
    const outputViewState = new Map();

    // The last full system payload seen on the WebSocket, which carries
    // everything the expanded row needs. Rendering from it avoids refetching —
    // and flashing "Loading…" over a pane someone is reading — every time a
    // check-in arrives.
    const detailPayloads = new Map();

    // What one host's pane keeps in the browser. Generous — this is a string in
    // memory, and scrolling back through a long upgrade is the point.
    const LIVE_OUTPUT_LIMIT = 512 * 1024;

    // A run whose client restarted mid-transaction never reports a result — the
    // package transaction survives in its own systemd unit, but the process that
    // was waiting on it is gone. After this long, stop believing a "running"
    // record and let the operator try again.
    const RUN_ABANDONED_MS = 45 * 60 * 1000;

    // Escape text for either element content or a double-quoted attribute value.
    // textContent/innerHTML alone does not touch quotes, which is fine in
    // content but lets a value carrying one break out of an attribute — and a
    // package-manager warning quoting a repository name reaches attributes.
    function escapeHtml(text) {
        if (text == null) return '';
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    }

    // Match a row by hostname. A hostname is data, not a fragment of selector
    // syntax: one carrying a quote or a bracket makes querySelector throw a
    // SyntaxError, which would take expand/collapse down with it. CSS.escape
    // renders arbitrary text as a valid unquoted attribute value.
    function hostnameAttr(hostname) {
        return `[data-hostname=${CSS.escape(hostname == null ? '' : String(hostname))}]`;
    }

    // Initialize WebSocket connection with exponential backoff
    function initWebSocket() {
        if (ws !== null && ws.readyState !== WebSocket.CLOSED) {
            return;
        }

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = `${protocol}//${window.location.host}/ws`;

        try {
            ws = new WebSocket(wsUrl);

            ws.onopen = () => {
                reconnectAttempts = 0; // Reset reconnection attempts on successful connection
                
                // Clear any existing ping interval
                if (pingIntervalId !== null) {
                    clearInterval(pingIntervalId);
                    pingIntervalId = null;
                }
                
                // Set up ping interval
                pingIntervalId = setInterval(() => {
                    if (ws !== null && ws.readyState === WebSocket.OPEN) {
                        ws.send('ping');
                    } else {
                        if (pingIntervalId !== null) {
                            clearInterval(pingIntervalId);
                            pingIntervalId = null;
                        }
                    }
                }, 30000); // Send ping every 30 seconds
            };

            ws.onmessage = (event) => {
                try {
                    // Ensure systemsData is always an array
                    if (!Array.isArray(systemsData)) {
                        systemsData = [];
                    }
                    
                    const update = JSON.parse(event.data);

                    // The socket carries two kinds of message. Live output is
                    // appended straight into the open pane: re-rendering the
                    // table for every line of a running upgrade would reload
                    // every expanded row several times a second.
                    if (update && update.type === "update_output") {
                        handleOutputChunk(update);
                        return;
                    }

                    // Validate that update has required fields
                    if (!update || !update.hostname) {
                        console.warn("Invalid WebSocket update received, missing hostname");
                        return;
                    }
                    
                    // A commanded check-in is answered by the payload it
                    // produces rather than by the request that asked for it.
                    resolveCheckIn(update);
                    resolvePendingReboot(update);
                    if (isRunActive(update.last_update_run)) startingUpdates.delete(update.hostname);

                    // Keep the payload: the expanded row renders from it rather
                    // than fetching the same thing again a millisecond later.
                    detailPayloads.set(update.hostname, { data: update, at: Date.now() });

                    // Update the systemsData array
                    const index = systemsData.findIndex(s => s && s.hostname === update.hostname);
                    if (index !== -1) {
                        systemsData[index] = update;
                    } else {
                        systemsData.push(update);
                    }
                    
                    // Render the updated systems
                    renderSystems(Array.from(systemsData));
                } catch (error) {
                    console.error("Failed to process WebSocket message:", error);
                }
            };

            ws.onclose = (event) => {
                clearWebSocket();
                
                // Implement exponential backoff for reconnection
                if (reconnectAttempts < MAX_RECONNECT_ATTEMPTS) {
                    const delay = Math.min(1000 * Math.pow(2, reconnectAttempts), 30000); // Cap at 30 seconds
                    setTimeout(() => {
                        reconnectAttempts++;
                        initWebSocket();
                    }, delay);
                } else {
                    console.error("Maximum reconnection attempts reached. Please refresh the page.");
                }
            };

            ws.onerror = (error) => {
                console.error("WebSocket error:", error);
            };

        } catch (error) {
            console.error("[ERROR] Failed to create WebSocket connection:", error);
            clearWebSocket();
        }
    }

    // Update all relative timestamps in the table
    function updateRelativeTimestamps() {
        const lastSeenCells = document.querySelectorAll('.last-seen-cell');
        lastSeenCells.forEach(cell => {
            const timestamp = cell.dataset.timestamp;
            if (timestamp) {
                const isStale = isStaleCheckIn(timestamp);
                const row = cell.closest('tr[data-hostname]');
                
                // Update row class
                if (row) {
                    if (isStale) {
                        row.classList.add('stale-checkin');
                    } else {
                        row.classList.remove('stale-checkin');
                    }
                }
                
                // Rebuild cell content with updated timestamp and stale indicator
                const relativeTime = formatRelativeTime(timestamp);
                if (isStale) {
                    cell.innerHTML = tooltipIcon('⚠️ ', 'stale-indicator', STALE_CHECKIN_TOOLTIP, 'right') + relativeTime;
                } else {
                    cell.textContent = relativeTime;
                }
                cell.dataset.tooltip = formatFullTimestamp(timestamp);
            }
        });
    }

    // Clean up WebSocket connection
    function clearWebSocket() {
        // Clear ping interval
        if (pingIntervalId !== null) {
            clearInterval(pingIntervalId);
            pingIntervalId = null;
        }
        
        // Clear timestamp update interval
        if (timestampUpdateIntervalId !== null) {
            clearInterval(timestampUpdateIntervalId);
            timestampUpdateIntervalId = null;
        }
        
        if (ws !== null) {
            // Remove all event listeners to prevent memory leaks
            ws.onopen = null;
            ws.onclose = null;
            ws.onmessage = null;
            ws.onerror = null;
            
            // Close the connection if it's still open
            if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
                ws.close();
            }
            ws = null;
        }
    }

    // Handle page visibility changes
    document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') {
            // Attempt to reconnect when page becomes visible
            if (ws === null || ws.readyState === WebSocket.CLOSED) {
                reconnectAttempts = 0; // Reset reconnection attempts
                initWebSocket();
            }
            // Update relative timestamps when page becomes visible
            updateRelativeTimestamps();
        }
    });

    // Handle page unload
    window.addEventListener('beforeunload', () => {
        clearWebSocket();
    });

    // Ask the server which optional capabilities it offers. A failure here is
    // not fatal: the dashboard simply renders without the controls it gates.
    function fetchFeatures() {
        return fetch("/api/features")
            .then((response) => {
                if (!response.ok) {
                    throw new Error(`Failed to fetch features: ${response.status}`);
                }
                return response.json();
            })
            .then((data) => {
                features = Object.assign({ remote_updates: false, remote_reboot: false }, data || {});
            })
            .catch((error) => {
                console.warn("Could not read server features; assuming none:", error);
            });
    }

    // ---------------------------------------------------------------------
    // Groups
    //
    // A group is server-side bookkeeping: a named set of hostnames the
    // dashboard can act on at once. Nothing about it reaches a host, and a
    // host does not know which groups it is in.
    //
    // Membership is deliberately not part of the /api/systems payload. It is
    // server-owned and changes by hand a few times a year, while a system
    // record is overwritten by its host every few minutes; keeping them apart
    // means a check-in can never clobber a group. The dashboard reads
    // /api/groups once at load and inverts it here.
    // ---------------------------------------------------------------------

    // The sentinel for "hosts in no group at all". A real group name can never
    // collide with it, because the API rejects "/" in a name.
    const UNGROUPED = "/ungrouped";

    let groupsData = [];
    let groupFilter = "";
    let lastGroupReport = null;
    // Which group's reboot checkbox is ticked. Held outside the DOM for the
    // same reason armedReboots is: the bar is re-rendered whenever a host
    // checks in, and a tick that only lived in the markup would be lost.
    const armedGroupReboots = new Set();
    // Hosts whose group membership is being saved. A tick saves at once, so
    // the host's boxes are held disabled until the save lands: a second tick
    // computed from the not-yet-updated groups would undo the first.
    const savingGroups = new Set();

    const GROUP_CHECK_IN_LABEL = "🔄 Check in all";
    const GROUP_UPDATE_LABEL = "⬇️ Run updates";
    const GROUP_REBOOT_LABEL = "⏻ Reboot group";

    // Ask the server for the groups. Like fetchFeatures, a failure here is not
    // fatal: the dashboard renders as a plain ungrouped list.
    function fetchGroups() {
        return fetch("/api/groups")
            .then((response) => {
                if (!response.ok) {
                    throw new Error(`Failed to fetch groups: ${response.status}`);
                }
                return response.json();
            })
            .then((data) => {
                groupsData = Array.isArray(data) ? data : [];
                // A group that has gone away must not keep filtering the table
                // down to nothing.
                if (groupFilter && groupFilter !== UNGROUPED && !findGroup(groupFilter)) {
                    groupFilter = "";
                }
            })
            .catch((error) => {
                console.warn("Could not read groups:", error);
                groupsData = [];
            });
    }

    // Group names are compared without regard to case, exactly as the server
    // compares them, so a chip always finds the group it was drawn from.
    function sameGroupName(a, b) {
        return String(a).toLowerCase() === String(b).toLowerCase();
    }

    function findGroup(name) {
        return groupsData.find((g) => g && sameGroupName(g.name, name)) || null;
    }

    function groupMembers(name) {
        const group = findGroup(name);
        return (group && Array.isArray(group.members)) ? group.members : [];
    }

    // The reverse index, computed rather than stored. At this scale it is a
    // handful of array scans, and a second persisted index would be one more
    // thing that can disagree with the first.
    function groupsForHost(hostname) {
        return groupsData
            .filter((g) => g && Array.isArray(g.members) && g.members.includes(hostname))
            .map((g) => g.name);
    }

    // Derived groups are computed by the server from what hosts report —
    // os:fedora, pkg:rpm, arch:x86_64, state:needs-reboot. They are told apart
    // by the flag rather than by the prefix, so the naming stays the server's
    // business.
    function isDerived(group) {
        return !!(group && group.derived);
    }

    function manualGroups() {
        return groupsData.filter((g) => !isDerived(g));
    }

    function derivedGroups() {
        return groupsData.filter(isDerived);
    }

    // Only the hand-made groups. Derived membership is not a thing anyone
    // chose, so it is not shown as a property of the host in the table, and
    // "Ungrouped" has to mean "not filed anywhere by hand" — otherwise it
    // would always be empty, since every host is in several derived groups.
    function manualGroupsForHost(hostname) {
        return manualGroups()
            .filter((g) => Array.isArray(g.members) && g.members.includes(hostname))
            .map((g) => g.name);
    }

    function filterByGroup(systems) {
        if (!groupFilter) return systems;
        if (groupFilter === UNGROUPED) {
            return systems.filter((s) => s && manualGroupsForHost(s.hostname).length === 0);
        }
        const members = groupMembers(groupFilter);
        return systems.filter((s) => s && members.includes(s.hostname));
    }

    // Members of the selected group that have no row in the table: a host that
    // has never checked in, or one whose row was deleted. They are named
    // rather than silently dropped, because "7 members, 5 rows" with nothing
    // to explain it is the kind of thing that costs an afternoon.
    function unknownMembers(name) {
        if (!name || name === UNGROUPED) return [];
        const known = new Set(systemsData.map((s) => s && s.hostname));
        return groupMembers(name).filter((m) => !known.has(m));
    }

    // The small group labels drawn beside a hostname. They go inside the
    // existing cell on purpose: the table's colspan is hardcoded in three
    // places and its header is hand-written, so a new column is a much larger
    // change than it looks.
    function groupChips(hostname) {
        // Deliberately not the derived ones: os:fedora and arch:x86_64 beside
        // every hostname would restate the OS and Architecture columns on
        // every row. The derived groups are useful as filters, not as labels.
        const names = manualGroupsForHost(hostname);
        if (!names.length) return '';
        return ' ' + names
            .map((n) => `<span class="host-group-chip">${escapeHtml(n)}</span>`)
            .join('');
    }

    function renderGroupBar() {
        const bar = document.getElementById("group-bar");
        if (!bar) return;

        const ungroupedCount = systemsData.filter((s) => s && manualGroupsForHost(s.hostname).length === 0).length;

        const chip = (group) => {
            const active = sameGroupName(groupFilter, group.name);
            const derived = isDerived(group);
            return `<button class="group-chip${derived ? ' derived' : ''}${active ? ' active' : ''}" ` +
                `data-filter="${escapeHtml(group.name)}"` +
                (derived ? ` title="Derived from what the hosts report. Membership cannot be edited."` : '') +
                `>${derived ? '◆' : ''}${escapeHtml(group.name)} ` +
                `<span class="group-count">${(group.members || []).length}</span></button>`;
        };

        const chips = [
            `<button class="group-chip${groupFilter === "" ? ' active' : ''}" data-filter="">All hosts <span class="group-count">${systemsData.length}</span></button>`,
        ];
        // Derived first, then the hand-made ones: the derived set is a fixed
        // reading of the fleet, while the groups below it are the ones someone
        // decided on.
        derivedGroups().forEach((group) => chips.push(chip(group)));
        manualGroups().forEach((group) => chips.push(chip(group)));
        if (ungroupedCount > 0) {
            chips.push(
                `<button class="group-chip${groupFilter === UNGROUPED ? ' active' : ''}" data-filter="${UNGROUPED}" ` +
                `title="Hosts in no hand-made group. Derived groups do not count — every host is in several.">` +
                `Ungrouped <span class="group-count">${ungroupedCount}</span></button>`
            );
        }
        chips.push(`<button class="group-chip new-group-btn" id="new-group-btn">+ New group</button>`);

        bar.innerHTML = chips.join('');
        renderGroupActions();
    }

    function renderGroupActions() {
        const el = document.getElementById("group-actions");
        if (!el) return;

        const group = (groupFilter && groupFilter !== UNGROUPED) ? findGroup(groupFilter) : null;
        if (!group) {
            el.innerHTML = '';
            el.style.display = 'none';
            renderGroupReport();
            return;
        }

        const members = group.members || [];
        const ghosts = unknownMembers(group.name);
        const ghostNote = ghosts.length
            ? ` <span class="group-ghost-note" title="${escapeHtml(ghosts.join(', '))}">${ghosts.length} not currently known</span>`
            : '';

        const updateBtn = features.remote_updates
            ? `<button class="group-action-btn" data-group-action="update"${members.length ? '' : ' disabled'}>${GROUP_UPDATE_LABEL}</button>`
            : '';

        // The same arm-checkbox the per-host reboot uses, and it looks
        // identical on purpose: a group reboot should feel like the control
        // the operator already knows rather than a new one to learn.
        const armed = armedGroupReboots.has(group.name.toLowerCase());
        const rebootControls = features.remote_reboot
            ? `<span class="reboot-controls">
                    <label class="reboot-arm${members.length ? '' : ' disabled'}">
                        <input type="checkbox" class="group-reboot-arm-checkbox"${armed ? ' checked' : ''}${members.length ? '' : ' disabled'}>
                        Confirm reboot
                    </label>
                    <button class="group-action-btn" data-group-action="reboot"${armed ? '' : ' disabled'}>${GROUP_REBOOT_LABEL}</button>
               </span>`
            : '';

        // A derived group has nothing to rename or delete: it exists for as
        // long as a host matches it and not a moment longer. Offering the
        // controls and refusing them would be worse than not offering them.
        const derived = isDerived(group);
        const editButtons = derived
            ? '<span class="group-derived-note" title="Membership is computed from what the hosts report, so it cannot be edited. The group disappears when nothing matches it.">derived from host facts</span>'
            : `<button class="group-edit-btn" data-group-edit="rename" title="Rename this group">Rename</button>
               <button class="group-edit-btn" data-group-edit="delete" title="Delete this group">Delete group</button>`;

        el.style.display = '';
        el.innerHTML = `
            <div class="group-action-summary">
                <strong>${derived ? '◆' : ''}${escapeHtml(group.name)}</strong>
                <span class="group-member-count">${members.length} ${members.length === 1 ? 'host' : 'hosts'}</span>${ghostNote}
            </div>
            <div class="group-action-buttons">
                <button class="group-action-btn" data-group-action="checkin"${members.length ? '' : ' disabled'}>${GROUP_CHECK_IN_LABEL}</button>
                ${updateBtn}
                ${rebootControls}
                ${editButtons}
            </div>
        `;
        renderGroupReport();
    }

    // The per-host outcomes of the last group action.
    //
    // Not an alert(): a reboot that skipped two hosts is something the operator
    // needs to keep reading while they go and look, and a modal is gone the
    // moment it is dismissed. The existing alerts are for a single host, where
    // there is exactly one sentence to say.
    function renderGroupReport() {
        const el = document.getElementById("group-action-report");
        if (!el) return;

        if (!lastGroupReport) {
            el.innerHTML = '';
            el.style.display = 'none';
            return;
        }

        const r = lastGroupReport;
        const verb = r.action === 'checkin' ? 'Check-in' : r.action === 'update' ? 'Update' : 'Reboot';
        const parts = [];
        if (r.accepted) parts.push(`${r.accepted} accepted`);
        if (r.skipped) parts.push(`${r.skipped} skipped`);
        if (r.failed) parts.push(`${r.failed} failed`);
        const summary = parts.length ? parts.join(' · ') : 'nothing to do';

        const lines = (r.results || []).map((res) => {
            const ghost = res.code === 'unknown_host' ? ' ghost' : '';
            return `<li>
                <span class="outcome-pill ${escapeHtml(res.outcome)}">${escapeHtml(res.outcome)}</span>
                <span class="outcome-host${ghost}">${escapeHtml(res.hostname)}</span>
                ${res.reason ? `<span class="outcome-reason">${escapeHtml(res.reason)}</span>` : ''}
            </li>`;
        }).join('');

        el.style.display = '';
        el.innerHTML = `
            <div class="group-report-head">
                <span>${verb} on <strong>${escapeHtml(r.group)}</strong> — ${escapeHtml(summary)}</span>
                <button class="group-report-dismiss" title="Dismiss">✕</button>
            </div>
            ${lines ? `<ul class="group-report-list">${lines}</ul>` : ''}
        `;
    }

    function handleGroupAction(action) {
        const group = findGroup(groupFilter);
        if (!group) return;

        if (action === 'reboot' && !armedGroupReboots.has(group.name.toLowerCase())) {
            // The button should have been disabled; treat it as if it were.
            return;
        }

        document.querySelectorAll('#group-actions .group-action-btn').forEach((b) => { b.disabled = true; });

        fetch(`/api/groups/${encodeURIComponent(group.name)}/${action}`, { method: 'POST' })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Group ${action} failed: ${response.status}`);
                }
                lastGroupReport = body;
                armedGroupReboots.delete(group.name.toLowerCase());

                // Seed the per-host pending state for everything that was
                // accepted, so each row shows the same spinner, timeout and
                // WebSocket-driven release it would for a single-host action.
                // A group action is N button presses and should look like it.
                (body.results || []).forEach((res) => {
                    if (res.outcome !== 'accepted') return;
                    const system = systemsData.find((s) => s && s.hostname === res.hostname);
                    if (action === 'checkin') {
                        markCheckInPending(res.hostname, (system && system.updates_checked_at) || '');
                    } else if (action === 'reboot') {
                        markRebootPending(res.hostname, (system && system.last_reboot && system.last_reboot.id) || '');
                    }
                });

                renderSystems(Array.from(systemsData));
            })
            .catch((error) => {
                console.error(`Group ${action} failed:`, error);
                lastGroupReport = null;
                renderGroupActions();
                alert(`Could not run ${action} on ${group.name}:\n\n${error.message}`);
            });
    }

    function handleNewGroup() {
        const name = prompt('Name for the new group:');
        if (name === null || !name.trim()) return;

        fetch('/api/groups', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: name.trim() }),
        })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Could not create the group: ${response.status}`);
                }
                return fetchGroups();
            })
            .then(() => renderSystems(Array.from(systemsData)))
            .catch((error) => alert(`Could not create the group:\n\n${error.message}`));
    }

    function handleRenameGroup() {
        const group = findGroup(groupFilter);
        if (!group) return;
        const name = prompt(`Rename "${group.name}" to:`, group.name);
        if (name === null || !name.trim() || name.trim() === group.name) return;

        fetch(`/api/groups/${encodeURIComponent(group.name)}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name: name.trim() }),
        })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Rename failed: ${response.status}`);
                }
                groupFilter = body.name || name.trim();
                return fetchGroups();
            })
            .then(() => renderSystems(Array.from(systemsData)))
            .catch((error) => alert(`Could not rename the group:\n\n${error.message}`));
    }

    function handleDeleteGroup() {
        const group = findGroup(groupFilter);
        if (!group) return;
        const count = (group.members || []).length;
        // A group is a label. Say so, so nobody reads this as deleting hosts.
        if (!confirm(`Delete the group "${group.name}"?\n\n` +
            `${count} ${count === 1 ? 'host is' : 'hosts are'} in it. They are not deleted, only the group is.`)) {
            return;
        }

        fetch(`/api/groups/${encodeURIComponent(group.name)}`, { method: 'DELETE' })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Delete failed: ${response.status}`);
                }
                groupFilter = '';
                lastGroupReport = null;
                return fetchGroups();
            })
            .then(() => renderSystems(Array.from(systemsData)))
            .catch((error) => alert(`Could not delete the group:\n\n${error.message}`));
    }

    // The per-host editor in the expanded row. Groups are a property of the
    // host rather than an action on it, so this sits with the system
    // information and not in the actions footer.
    function groupEditorHTML(hostname) {
        const manual = manualGroups();
        const selected = new Set(manualGroupsForHost(hostname));
        const saving = savingGroups.has(hostname);

        // The derived memberships are shown but not offered as checkboxes: a
        // host joins os:fedora by being a Fedora box, and a tickbox that
        // reverted on the next read would be worse than no tickbox.
        const derivedNames = derivedGroups()
            .filter((g) => Array.isArray(g.members) && g.members.includes(hostname))
            .map((g) => g.name);
        const derivedHTML = derivedNames.length
            ? `<div class="group-editor-derived">
                ${derivedNames.map((n) => `<span class="host-group-chip derived">◆${escapeHtml(n)}</span>`).join('')}
            </div>`
            : '';

        // Each box saves as it is ticked. Membership is cheap to change and
        // easy to change back, so a separate Save step only added a click and
        // a way to lose an edit.
        const boxes = manual.length
            ? manual.map((group) => `
                <label class="group-editor-option${saving ? ' saving' : ''}">
                    <input type="checkbox" class="group-member-checkbox"
                           data-hostname="${escapeHtml(hostname)}"
                           data-group="${escapeHtml(group.name)}"${selected.has(group.name) ? ' checked' : ''}${saving ? ' disabled' : ''}>
                    ${escapeHtml(group.name)}
                </label>`).join('')
            : '<span class="group-editor-empty">No groups of your own yet. Make one with + New group above the table.</span>';

        return `<div class="group-editor">
            <div class="group-editor-options">${boxes}</div>
            ${derivedHTML}
        </div>`;
    }

    function handleGroupMemberToggle(checkbox) {
        const hostname = checkbox.dataset.hostname;
        const group = checkbox.dataset.group;
        if (!hostname || !group || savingGroups.has(hostname)) return;

        const selected = new Set(manualGroupsForHost(hostname));
        if (checkbox.checked) {
            selected.add(group);
        } else {
            selected.delete(group);
        }

        savingGroups.add(hostname);
        checkbox.closest('.group-editor')
            .querySelectorAll('.group-member-checkbox')
            .forEach((box) => { box.disabled = true; });

        fetch(`/api/systems/${encodeURIComponent(hostname)}/groups`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ groups: Array.from(selected) }),
        })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Could not save groups: ${response.status}`);
                }
                return fetchGroups();
            })
            .catch((error) => {
                console.error(`Failed to save groups for ${hostname}:`, error);
                alert(`Could not save groups for ${hostname}:\n\n${error.message}`);
            })
            .finally(() => {
                savingGroups.delete(hostname);
                renderSystems(Array.from(systemsData));
            });
    }

    // Delegated listeners on the bar, the action strip and the report. They
    // are attached once at boot rather than on every render, because all three
    // are rebuilt whenever any host checks in.
    function initGroupControls() {
        const bar = document.getElementById("group-bar");
        if (bar) {
            bar.addEventListener('click', (event) => {
                if (event.target.closest('#new-group-btn')) {
                    handleNewGroup();
                    return;
                }
                const chip = event.target.closest('.group-chip');
                if (!chip || chip.dataset.filter === undefined) return;
                groupFilter = chip.dataset.filter;
                lastGroupReport = null;
                armedGroupReboots.clear();
                renderSystems(Array.from(systemsData));
            });
        }

        const actions = document.getElementById("group-actions");
        if (actions) {
            actions.addEventListener('click', (event) => {
                const edit = event.target.closest('[data-group-edit]');
                if (edit) {
                    if (edit.dataset.groupEdit === 'rename') handleRenameGroup();
                    else handleDeleteGroup();
                    return;
                }
                const btn = event.target.closest('[data-group-action]');
                if (btn && !btn.disabled) handleGroupAction(btn.dataset.groupAction);
            });
            actions.addEventListener('change', (event) => {
                const checkbox = event.target.closest('.group-reboot-arm-checkbox');
                if (!checkbox) return;
                const group = findGroup(groupFilter);
                if (!group) return;
                if (checkbox.checked) {
                    armedGroupReboots.add(group.name.toLowerCase());
                } else {
                    armedGroupReboots.delete(group.name.toLowerCase());
                }
                const button = actions.querySelector('[data-group-action="reboot"]');
                if (button) button.disabled = !checkbox.checked;
            });
        }

        const report = document.getElementById("group-action-report");
        if (report) {
            report.addEventListener('click', (event) => {
                if (!event.target.closest('.group-report-dismiss')) return;
                lastGroupReport = null;
                renderGroupReport();
            });
        }
    }

    // Fetch and render systems list
    function fetchSystems() {
        fetch("/api/systems")
            .then((response) => {
                if (!response.ok) {
                    throw new Error(`Failed to fetch systems: ${response.status}`);
                }
                return response.json();
            })
            .then((systems) => {
                // Ensure systemsData is always an array
                systemsData = Array.isArray(systems) ? systems : [];
                renderSystems(systemsData);
            })
            .catch((error) => {
                console.error("Failed to fetch systems:", error);
                // Ensure systemsData is initialized even on error
                if (!Array.isArray(systemsData)) {
                    systemsData = [];
                }
                renderSystems(systemsData);
            });
    }

    // Format timestamp to full readable format (for tooltips)
    function formatFullTimestamp(isoTimestamp) {
        if (!isoTimestamp) return '';
        const date = new Date(isoTimestamp);
        const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
        const day = date.getUTCDate().toString().padStart(2, '0');
        const month = months[date.getUTCMonth()];
        const year = date.getUTCFullYear();
        const hours = date.getUTCHours().toString().padStart(2, '0');
        const minutes = date.getUTCMinutes().toString().padStart(2, '0');
        const seconds = date.getUTCSeconds().toString().padStart(2, '0');
        
        return `${day} ${month} ${year} ${hours}:${minutes}:${seconds} UTC`;
    }

    // Format timestamp to human-readable relative time
    function formatRelativeTime(isoTimestamp) {
        if (!isoTimestamp) return 'never';
        
        const now = new Date();
        const then = new Date(isoTimestamp);
        const diffMs = now - then;
        const diffSeconds = Math.floor(diffMs / 1000);
        const diffMinutes = Math.floor(diffSeconds / 60);
        const diffHours = Math.floor(diffMinutes / 60);
        const diffDays = Math.floor(diffHours / 24);
        
        // Handle future dates (shouldn't happen, but just in case)
        if (diffMs < 0) {
            return 'in the future';
        }
        
        // Less than 10 seconds
        if (diffSeconds < 10) {
            return 'just now';
        }
        
        // Less than 1 minute
        if (diffSeconds < 60) {
            return 'about a minute ago';
        }
        
        // Less than 2 minutes
        if (diffMinutes < 2) {
            return 'about a minute ago';
        }
        
        // Less than 1 hour
        if (diffMinutes < 60) {
            return `${diffMinutes} minute${diffMinutes === 1 ? '' : 's'} ago`;
        }
        
        // Less than 24 hours
        if (diffHours < 24) {
            if (diffHours === 1) {
                return 'about an hour ago';
            }
            return `about ${diffHours} hour${diffHours === 1 ? '' : 's'} ago`;
        }
        
        // Less than 7 days
        if (diffDays < 7) {
            if (diffDays === 1) {
                return 'about a day ago';
            }
            return `about ${diffDays} day${diffDays === 1 ? '' : 's'} ago`;
        }
        
        // Less than 30 days
        const diffWeeks = Math.floor(diffDays / 7);
        if (diffDays < 30) {
            if (diffWeeks === 1) {
                return 'about a week ago';
            }
            return `about ${diffWeeks} week${diffWeeks === 1 ? '' : 's'} ago`;
        }
        
        // Less than 365 days
        const diffMonths = Math.floor(diffDays / 30);
        if (diffDays < 365) {
            if (diffMonths === 1) {
                return 'about a month ago';
            }
            return `about ${diffMonths} month${diffMonths === 1 ? '' : 's'} ago`;
        }
        
        // More than a year
        const diffYears = Math.floor(diffDays / 365);
        if (diffYears === 1) {
            return 'about a year ago';
        }
        return `about ${diffYears} year${diffYears === 1 ? '' : 's'} ago`;
    }

    // Format a byte count as a human-readable RAM amount (e.g. "32 GB").
    function formatBytes(bytes) {
        if (!bytes || bytes <= 0) return '';
        const gib = bytes / (1024 ** 3);
        if (gib >= 1) {
            return `${Math.round(gib)} GB`;
        }
        return `${Math.round(bytes / (1024 ** 2))} MB`;
    }

    // Format an uptime in seconds as the two most significant units
    // (e.g. "14d 3h", "3h 12m", "5m").
    function formatUptime(seconds) {
        if (!seconds || seconds <= 0) return '';
        const days = Math.floor(seconds / 86400);
        const hours = Math.floor((seconds % 86400) / 3600);
        const minutes = Math.floor((seconds % 3600) / 60);
        if (days > 0) return `${days}d ${hours}h`;
        if (hours > 0) return `${hours}h ${minutes}m`;
        return `${minutes}m`;
    }

    function getUpdatePriority(updates) {
        if (!updates || updates.length === 0) return 'none';
        // Check for security updates
        const hasSecurityUpdates = updates.some(u =>
            u.name.toLowerCase().includes('security') ||
            u.source.toLowerCase().includes('security')
        );
        if (hasSecurityUpdates) return 'high';
        // Any updates (non-security) are medium priority (yellow)
        return 'medium';
    }

    // Hours after which data is considered stale, for both check-in and update data.
    const STALE_THRESHOLD_HOURS = 4;

    // Written out once because the stale marker is rendered from two places: the
    // row template, and the timer that refreshes relative timestamps in place.
    const STALE_CHECKIN_TOOLTIP = `Host has not checked in for ${STALE_THRESHOLD_HOURS}+ hours`;

    // Check if a system hasn't checked in for 4+ hours
    function isStaleCheckIn(isoTimestamp) {
        if (!isoTimestamp) return true; // Consider missing timestamp as stale

        const now = new Date();
        const then = new Date(isoTimestamp);
        const diffMs = now - then;
        const diffHours = diffMs / (1000 * 60 * 60);

        return diffHours >= STALE_THRESHOLD_HOURS;
    }

    // Check whether a system's update data is old even though the host itself is
    // still checking in. last_seen is stamped by the server on receipt, so it
    // only says the host is reachable; updates_checked_at is when the client
    // actually queried its package manager. The two diverge when a client keeps
    // publishing while its package-manager check is failing or serving from
    // long-expired metadata — the case where the badge looks trustworthy but the
    // data behind it is not.
    function isStaleUpdateData(system) {
        if (!system || !system.updates_checked_at || !system.last_seen) return false;

        const checked = new Date(system.updates_checked_at);
        const seen = new Date(system.last_seen);
        if (isNaN(checked) || isNaN(seen)) return false;

        return (seen - checked) / (1000 * 60 * 60) >= STALE_THRESHOLD_HOURS;
    }

    // Build the tooltip/label for a system whose update check was incomplete.
    function updateWarningText(system) {
        if (!system || !system.update_check_warnings || system.update_check_warnings.length === 0) {
            return '';
        }
        return system.update_check_warnings.join('; ');
    }

    // Roughly how many characters of tooltip fit across the table on one line.
    // Tooltips are drawn on a single line so that one hovered in the first row
    // does not reach past the top of the table and get clipped, which puts a
    // budget on their width instead. See the [data-tooltip] rules.
    const TOOLTIP_MAX_LENGTH = 110;

    // Keep a tooltip inside that budget. Nothing is lost by cutting one short:
    // the full text is in the host's expanded details either way.
    function truncateTooltip(text) {
        if (!text || text.length <= TOOLTIP_MAX_LENGTH) return text || '';
        return text.slice(0, TOOLTIP_MAX_LENGTH - 1).trimEnd() + '…';
    }

    // Name as many pending updates as fit on the line and count the rest. A
    // host with two hundred of them is common, and naming them all produced a
    // tooltip several screens wide that said less than this one does.
    function pendingUpdatesTooltip(pending) {
        const names = Array.isArray(pending)
            ? pending.map(update => (update && update.name) || '').filter(Boolean)
            : [];
        if (names.length === 0) return 'Updates available - click for details';

        const prefix = 'Updates available: ';
        const shown = [];
        let length = prefix.length;
        for (const name of names) {
            // Always name at least one, however long it is; truncateTooltip
            // catches a pathologically long package name.
            if (shown.length && length + name.length + 2 > TOOLTIP_MAX_LENGTH) break;
            shown.push(name);
            length += name.length + 2;
        }
        const remaining = names.length - shown.length;
        return truncateTooltip(prefix + shown.join(', ')) +
            (remaining > 0 ? `, and ${remaining} more` : '');
    }

    // Markup for an icon that says nothing on its own: the tooltip is the whole
    // message, so it doubles as the icon's accessible name. aria-label gets the
    // full text — only the drawn panel has a width to stay inside.
    //
    // align is "right" for the columns far enough across the table that a
    // left-anchored panel would be clipped by its right edge.
    function tooltipIcon(icon, className, tooltip, align) {
        return `<span class="${className}" role="img" aria-label="${escapeHtml(tooltip)}"` +
            ` data-tooltip="${escapeHtml(truncateTooltip(tooltip))}"` +
            `${align ? ` data-tooltip-align="${align}"` : ''}>${icon}</span>`;
    }

    // Marks an update badge whose data is incomplete or old, and says why.
    function dataWarningIndicator(reason) {
        return tooltipIcon(' ⚠', 'data-warning-indicator', reason, 'right');
    }

    // Explain a disconnected tailnet dot in the host's own terms.
    function tailnetStateReason(state) {
        switch (state) {
            case 'Stopped': return 'Tailscale is stopped';
            case 'NeedsLogin': return 'Tailscale is logged out';
            case 'Starting':
            case 'NoState': return 'Tailscale is starting up';
            case 'unreported': return 'host is no longer reporting Tailscale';
            default: return '';
        }
    }

    // Best available name for the tailnet a host belongs to: the current one
    // when connected, otherwise the one it was last seen on.
    function tailnetName(ts) {
        if (!ts) return '';
        if (ts.connected) return ts.tailnet || ts.magic_dns_suffix || '';
        return ts.last_tailnet || ts.tailnet || ts.magic_dns_suffix || '';
    }

    // Hover text for the tailnet dot. A host can belong to several tailnets but
    // join only one at a time, so naming the tailnet is the whole point of the
    // indicator — the dot alone only says connected or not.
    //
    // The name is all the tooltip says: the address, the MagicDNS suffix and
    // why a host is disconnected are all in the expanded details, and a tooltip
    // that recites them makes the one fact worth glancing at harder to read.
    // A grey dot still gets "not connected" so the name cannot be misread as
    // where the host is right now.
    function tailnetTooltip(ts) {
        const name = tailnetName(ts);
        if (ts.connected) return name ? `tailnet: ${name}` : 'on a tailnet (name unavailable)';
        return name ? `tailnet: ${name} — not connected` : 'not on a tailnet';
    }

    // Render the tailnet dot that leads a hostname cell.
    //
    // The dot sits in a fixed-width slot so hostnames line up down the column
    // whether or not a given host has one. The slot is only laid out at all
    // when some host in the table reports a tailnet — a fleet that does not use
    // Tailscale gets neither dots nor the space they would occupy. The slot,
    // not the 8px dot, carries the tooltip: it is a much easier hover target.
    //
    // The tooltip rides on data-tooltip and is drawn by CSS rather than by a
    // title attribute, for the reason the [data-tooltip] rules give; aria-label
    // carries the full text for anyone not using a pointer.
    function tailnetIndicator(system, reserveSlot) {
        if (!reserveSlot) return '';
        const ts = system && system.tailscale;
        if (!ts) return '<span class="tailnet-slot"></span>';
        const tooltip = tailnetTooltip(ts);
        return `<span class="tailnet-slot" role="img" data-tooltip="${escapeHtml(truncateTooltip(tooltip))}"` +
            ` aria-label="${escapeHtml(tooltip)}">` +
            `<span class="tailnet-indicator${ts.connected ? ' connected' : ''}"></span></span>`;
    }

    // Spell out in the expanded details what the dot only hints at, leading
    // with the tailnet name — that is the part a dot cannot convey.
    //
    // This is where the whole of what the host reported lands, since the dot's
    // tooltip now says only which tailnet it is: the MagicDNS suffix and the
    // address hang off the name, each held on one line, since a wrapped domain
    // or address reads as a separate fact rather than a detail of the same one.
    // The suffix is skipped when it is standing in as the name, so the same
    // string is not printed twice.
    function tailnetDetail(system) {
        const ts = system && system.tailscale;
        if (!ts) return '';
        const name = tailnetName(ts);
        const detail = value => ` <span class="tailnet-address">· ${escapeHtml(value)}</span>`;
        let html = `<span class="tailnet-indicator${ts.connected ? ' connected' : ''}"></span>`;
        if (ts.connected) {
            html += escapeHtml(name || 'connected (tailnet name unavailable)');
            if (ts.magic_dns_suffix && ts.magic_dns_suffix !== name) html += detail(ts.magic_dns_suffix);
            if (ts.ip) html += detail(ts.ip);
        } else {
            html += escapeHtml(name ? `${name} — not connected` : 'not connected');
            if (ts.magic_dns_suffix && ts.magic_dns_suffix !== name) html += detail(ts.magic_dns_suffix);
            const reason = tailnetStateReason(ts.state);
            if (reason) html += escapeHtml(` · ${reason}`);
            if (ts.last_connected_at) html += escapeHtml(` · last connected ${formatRelativeTime(ts.last_connected_at)}`);
        }
        return html;
    }

    // Get OS icon based on OS name
    function getOSIcon(os) {
        if (!os) return '';
        
        const osLower = os.toLowerCase();
        
        // macOS
        if (osLower.includes('darwin') || osLower.includes('macos') || osLower.includes('mac os')) {
            return '<img class="os-icon" src="/static/images/macos-30.png" alt="macOS" width="20" height="20">';
        }
        
        // Debian
        if (osLower.includes('debian')) {
            return '<img class="os-icon" src="/static/images/debian-48.png" alt="Debian" width="20" height="20">';
        }

        // Pop!_OS - check before Ubuntu since Pop reports PRETTY_NAME with "Pop!_OS"
        if (osLower.includes('pop!_os') || osLower.includes('pop_os') || osLower.includes('pop-os') || osLower.includes('pop os')) {
            return '<img class="os-icon" src="/static/images/pop-os-48.png" alt="Pop!_OS" width="20" height="20">';
        }
        
        // Fedora
        if (osLower.includes('fedora')) {
            return '<img class="os-icon" src="/static/images/fedora-48.png" alt="Fedora" width="20" height="20">';
        }
        
        // Red Hat / RHEL
        if (osLower.includes('red hat') || osLower.includes('rhel') || osLower.includes('redhat')) {
            return '<img class="os-icon" src="/static/images/red-hat-48.png" alt="Red Hat" width="20" height="20">';
        }
        
        // Ubuntu
        if (osLower.includes('ubuntu')) {
            return '<img class="os-icon" src="/static/images/ubuntu-48.png" alt="Ubuntu" width="20" height="20">';
        }

        // SUSE / openSUSE
        if (osLower.includes('suse') || osLower.includes('opensuse')) {
            return '<img class="os-icon" src="/static/images/suse-48.png" alt="SUSE" width="20" height="20">';
        }

        // Rocky Linux
        if (osLower.includes('rocky')) {
            return '<img class="os-icon" src="/static/images/rocky-48.png" alt="Rocky Linux" width="20" height="20">';
        }

        // CentOS - check before generic 'centos stream' etc.
        if (osLower.includes('centos')) {
            return '<img class="os-icon" src="/static/images/centos-48.png" alt="CentOS" width="20" height="20">';
        }

        // Arch Linux
        if (osLower.includes('arch')) {
            return '<img class="os-icon" src="/static/images/arch-48.png" alt="Arch Linux" width="20" height="20">';
        }

        // Gentoo
        if (osLower.includes('gentoo')) {
            return '<img class="os-icon" src="/static/images/gentoo-48.png" alt="Gentoo" width="20" height="20">';
        }

        // NixOS
        if (osLower.includes('nixos')) {
            return '<img class="os-icon" src="/static/images/nixos.png" alt="NixOS" width="20" height="20">';
        }
        
        // Generic Linux fallback - use Linux icon
        if (osLower.includes('linux') || osLower.includes('armbian')) {
            return '<img class="os-icon" src="/static/images/linux-48.png" alt="Linux" width="20" height="20">';
        }
        
        // Default/unknown OS
        return '';
    }

    // Render systems data into the table
    function renderSystems(systems) {
        // Ensure systemsTable exists
        if (!systemsTable) {
            console.error("Systems table element not found!");
            return;
        }
        
        // Handle null/undefined systems array
        if (!systems || !Array.isArray(systems)) {
            systems = [];
        }

        // The chip bar counts the whole fleet, so it is drawn from the
        // unfiltered list before the filter is applied below.
        renderGroupBar();

        // One row per host, always: the table is filtered rather than divided
        // into sections. A host in several groups would otherwise be drawn
        // several times, and every document.querySelector('[data-hostname=...]')
        // in this file would then drive only the first copy.
        const unfilteredCount = systems.length;
        systems = filterByGroup(systems);

        // An empty group is a different thing from an empty fleet, and saying
        // "no systems have checked in yet" under a group whose members simply
        // have no rows yet would be a lie.
        if (systems.length === 0 && unfilteredCount > 0) {
            const label = groupFilter === UNGROUPED ? 'Ungrouped' : groupFilter;
            systemsTable.innerHTML = `
                <tr>
                    <td colspan="9" style="text-align: center; padding: 3rem; color: var(--text-secondary);">
                        <div style="font-size: 16px; margin-bottom: 8px;">\u{1F50D}</div>
                        <div style="font-weight: 500; margin-bottom: 4px;">No hosts to show in ${escapeHtml(label)}</div>
                        <div style="font-size: 13px; opacity: 0.8;">Its members may not have checked in yet</div>
                    </td>
                </tr>
            `;
            return;
        }

        // Show message if no systems exist yet
        if (systems.length === 0) {
            systemsTable.innerHTML = `
                <tr>
                    <td colspan="9" style="text-align: center; padding: 3rem; color: var(--text-secondary);">
                        <div style="font-size: 16px; margin-bottom: 8px;">📡</div>
                        <div style="font-weight: 500; margin-bottom: 4px;">No systems have checked in yet</div>
                        <div style="font-size: 13px; opacity: 0.8;">Systems will appear here automatically as they connect</div>
                    </td>
                </tr>
            `;
            return;
        }
        // Sort the systems based on the current sort order
        systems.sort((a, b) => {
            const aRaw = a[sortOrder.column];
            const bRaw = b[sortOrder.column];
            // Numeric columns (cores, RAM, uptime) must sort numerically, not
            // lexically ("9" vs "13").
            if (typeof aRaw === "number" && typeof bRaw === "number") {
                return sortOrder.ascending ? aRaw - bRaw : bRaw - aRaw;
            }
            const aValue = (aRaw ?? "").toString();
            const bValue = (bRaw ?? "").toString();
            if (sortOrder.ascending) {
                return aValue.localeCompare(bValue);
            } else {
                return bValue.localeCompare(aValue);
            }
        });

        // Lay out the tailnet slot only when the fleet actually uses Tailscale,
        // so hostnames line up with each other either way.
        const showTailnetSlot = systems.some(system => system && system.tailscale);

        // Generate table rows
        try {
            const rows = systems
                .map(
                    (system) => {
                        if (!system || !system.hostname) {
                            return '';
                        }
                        const isStale = isStaleCheckIn(system.last_seen);
                        const staleData = isStaleUpdateData(system);
                        const warning = updateWarningText(system);
                        // A badge is only as good as the data behind it. Mark it
                        // when the check was incomplete (a repo was skipped) or
                        // when the host is reachable but its update data is old.
                        const badgeNote = warning
                            ? dataWarningIndicator(warning)
                            : staleData
                            ? dataWarningIndicator(`Host is checking in, but its update data was collected ${formatRelativeTime(system.updates_checked_at)}`)
                            : '';
                        return `
                    <tr data-hostname="${escapeHtml(system.hostname)}"${isStale ? ' class="stale-checkin"' : ''}>
                        <td class="chevron-cell"><button class="chevron" aria-expanded="false" aria-label="Details for ${escapeHtml(system.hostname)}">▶</button></td>
                        <td>${tailnetIndicator(system, showTailnetSlot)}${escapeHtml(system.hostname)}${rebootIndicator(system)}${groupChips(system.hostname)}</td>
                        <td class="os-cell">${getOSIcon(system.os)} <span class="os-text">${escapeHtml(system.os || '')} ${escapeHtml(system.os_version || '')}</span></td>
                        <td>${escapeHtml(system.architecture || '')}</td>
                        <td>${escapeHtml(system.ip || '')}</td>
                        <td>${system.update_status_unknown ?
                            `<span class="update-badge status-unknown" data-tooltip-align="right"
                                   data-tooltip="${escapeHtml(truncateTooltip(warning || 'Package manager not detected - update status unknown'))}">
                                Status unknown
                            </span>` :
                            system.updates_available ?
                            `<span class="update-badge update-available${system.pending_updates ? ' priority-' + getUpdatePriority(system.pending_updates) : ''}"
                                   data-tooltip-align="right"
                                   data-tooltip="${escapeHtml(pendingUpdatesTooltip(system.pending_updates))}">
                                Updates${system.pending_updates ? ` (${system.pending_updates.length})` : ' (click for details)'}
                                ${system.pending_updates && getUpdatePriority(system.pending_updates) === 'high' ? ' ⚠️' : ''}
                            </span>${badgeNote}` :
                            `<span class="update-badge up-to-date">Up to date</span>${badgeNote}`
                        }${runningIndicator(system)}</td>
                        <td>${escapeHtml(formatUptime(system.uptime_seconds))}</td>
                        <td class="last-seen-cell" data-timestamp="${escapeHtml(system.last_seen || '')}" data-tooltip-align="right" data-tooltip="${escapeHtml(formatFullTimestamp(system.last_seen || ''))}">
                            ${isStale ? tooltipIcon('⚠️ ', 'stale-indicator', STALE_CHECKIN_TOOLTIP, 'right') : ''}
                            ${formatRelativeTime(system.last_seen || '')}
                        </td>
                        <td class="actions-cell">${rowActionsHTML(system)}</td>
                    </tr>
                    <tr class="details-row" data-hostname="${escapeHtml(system.hostname)}" style="display: none;">
                        <td colspan="9">
                            <div class="details-content">Loading...</div>
                        </td>
                    </tr>`;
                    }
                )
                .filter(row => row !== '') // Remove empty rows from invalid systems
                .join("");
            
            systemsTable.innerHTML = rows;
        } catch (error) {
            console.error("Failed to generate table rows:", error);
            systemsTable.innerHTML = `
                <tr>
                    <td colspan="9" style="text-align: center; padding: 3rem; color: var(--accent-red);">
                        <div style="font-size: 16px; margin-bottom: 8px;">⚠️</div>
                        <div style="font-weight: 500; margin-bottom: 4px;">Error rendering systems</div>
                        <div style="font-size: 13px; opacity: 0.8;">${escapeHtml(error.message)}</div>
                    </td>
                </tr>
            `;
            return;
        }

        // Restore expanded rows for systems that were manually expanded
        expandedSystems.forEach(hostname => {
            const detailsRow = document.querySelector(`.details-row${hostnameAttr(hostname)}`);
            const chevron = document.querySelector(`tr${hostnameAttr(hostname)} .chevron`);
            if (detailsRow && chevron) {
                detailsRow.style.display = "table-row";
                setChevron(chevron, true);
                // Load details if not already loaded
                const detailsContent = detailsRow.querySelector(".details-content");
                if (!detailsContent.dataset.loaded || Date.now() - detailsContent.dataset.loadedTime > 60000) {
                    loadSystemDetails(hostname, detailsContent);
                }
            }
        });
    }

    // Helper function to calculate stale days
    function getStaleDays(isoTimestamp) {
        if (!isoTimestamp) return Infinity;
        const now = new Date();
        const then = new Date(isoTimestamp);
        const diffMs = now - then;
        return Math.floor(diffMs / (1000 * 60 * 60 * 24));
    }

    // An update run is only believed to be in progress for as long as a run
    // plausibly takes; see RUN_ABANDONED_MS.
    function isRunActive(run) {
        if (!run || run.status !== "running") return false;
        const started = Date.parse(run.started_at || "");
        if (isNaN(started)) return true;
        return Date.now() - started < RUN_ABANDONED_MS;
    }

    // A reboot is believed to be in progress from the host's own record until
    // its next check-in closes it — or until it has been long enough that a
    // spinner would be a lie; see REBOOT_OVERDUE_MS.
    function isRebooting(reboot) {
        return !!reboot && reboot.status === "rebooting";
    }

    function isRebootOverdue(reboot) {
        if (!isRebooting(reboot)) return false;
        const requested = Date.parse(reboot.requested_at || "");
        if (isNaN(requested)) return false;
        return Date.now() - requested > REBOOT_OVERDUE_MS;
    }

    // The mark beside the hostname: the reboot in progress takes the place of
    // the "reboot required" flag it is about to clear, and turns into a warning
    // once the host is overdue.
    function rebootIndicator(system) {
        const reboot = system && system.last_reboot;
        if (isRebootOverdue(reboot)) {
            return ' ' + tooltipIcon('\u23fb', 'rebooting-indicator overdue',
                `Reboot requested ${formatRelativeTime(reboot.requested_at || '')}; the host has not checked in since`);
        }
        if (isRebooting(reboot)) {
            return ' ' + tooltipIcon('\u23fb', 'rebooting-indicator',
                `Rebooting \u2014 requested ${formatRelativeTime(reboot.requested_at || '')}`);
        }
        if (system && system.reboot_required) {
            return ' ' + tooltipIcon('\u27f3', 'reboot-indicator', 'Reboot required');
        }
        return '';
    }

    // A small indicator in the table so a run in progress is visible without
    // expanding the row.
    function runningIndicator(system) {
        if (!isRunActive(system && system.last_update_run)) return '';
        return ' ' + tooltipIcon('\u23f3', 'update-running-indicator', 'An update is running on this host', 'right');
    }

    // Append one streamed chunk to a host's buffer, and to its open pane if the
    // row happens to be expanded. Chunks are numbered so a drop — the server
    // sheds them rather than queueing without limit — shows as a gap instead of
    // silently splicing two unrelated moments together.
    function handleOutputChunk(message) {
        const hostname = message.hostname;
        if (!hostname) return;

        let state = liveOutput.get(hostname);
        if (!state || state.id !== message.id) {
            state = { id: message.id, seq: 0, text: "" };
            liveOutput.set(hostname, state);
        }

        let text = message.chunk || "";
        if (state.seq && message.seq !== state.seq + 1) {
            text = "\n[\u2026 some live output was dropped \u2026]\n" + text;
        }
        state.seq = message.seq;
        state.text += text;
        if (state.text.length > LIVE_OUTPUT_LIMIT) {
            state.text = "[earlier output truncated]\n" +
                state.text.slice(state.text.length - LIVE_OUTPUT_LIMIT);
        }

        appendToOutputPane(hostname, text);
    }

    // Write into the pane in place. Scrolling follows the output only when the
    // reader is already at the bottom, so scrolling back to read something does
    // not fight the stream.
    function appendToOutputPane(hostname, text) {
        const detailsRow = document.querySelector(`.details-row${hostnameAttr(hostname)}`);
        if (!detailsRow || detailsRow.style.display === "none") return;

        const pre = detailsRow.querySelector('.update-run-output pre');
        if (!pre) return;

        const placeholder = pre.querySelector('.update-run-waiting');
        if (placeholder) placeholder.remove();

        const atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
        pre.appendChild(document.createTextNode(text));
        if (atBottom) {
            pre.scrollTop = pre.scrollHeight;
        }
    }

    // Seed the pane for a run that started before this page was looking. Without
    // it, opening a row (or reloading) mid-run shows an empty pane until the
    // next chunk happens to arrive, which reads as "nothing is happening".
    function seedLiveOutput(hostname, run) {
        if (!run || !isRunActive(run)) return;
        const state = liveOutput.get(hostname);
        if (state && state.id === run.id) return;

        fetch(`/api/systems/${encodeURIComponent(hostname)}/update/output`)
            .then((response) => (response.ok ? response.json() : null))
            .then((snapshot) => {
                if (!snapshot || snapshot.id !== run.id) return;
                // A chunk may have landed while this was in flight; the seeded
                // history is only useful if it is still the older half.
                const current = liveOutput.get(hostname);
                if (current && current.id === run.id) return;

                liveOutput.set(hostname, {
                    id: snapshot.id,
                    seq: snapshot.seq,
                    text: (snapshot.truncated ? "[earlier output truncated]\n" : "") + (snapshot.output || ""),
                });
                const detailsRow = document.querySelector(`.details-row${hostnameAttr(hostname)}`);
                const pre = detailsRow && detailsRow.querySelector('.update-run-output pre');
                if (pre) {
                    pre.textContent = liveOutput.get(hostname).text;
                    pre.scrollTop = pre.scrollHeight;
                }
            })
            .catch((error) => {
                console.debug(`No live output to seed for ${hostname}:`, error);
            });
    }

    // What the pane shows: the live buffer while it belongs to this run, and the
    // saved tail otherwise. The live buffer is the fuller of the two, so a run
    // watched from the start stays fully readable after it ends.
    function runOutputText(hostname, run) {
        const state = liveOutput.get(hostname);
        if (state && run && state.id === run.id) return state.text;
        return (run && run.output) || '';
    }

    // The record of the last dashboard-triggered update run, shown in the
    // expanded details. The output tail is collapsed: it matters when something
    // failed and is noise when it did not.
    function updateRunHTML(hostname, run) {
        if (!run) return '';

        const active = isRunActive(run);
        const abandoned = !active && run.status === "running";
        let heading;
        if (active) {
            heading = `\u23f3 Update running &mdash; started ${escapeHtml(formatRelativeTime(run.started_at || ''))}`;
        } else if (abandoned) {
            heading = `\u2753 Update result unknown &mdash; started ${escapeHtml(formatRelativeTime(run.started_at || ''))}`;
        } else if (run.status === "succeeded") {
            heading = `\u2705 Update succeeded ${escapeHtml(formatRelativeTime(run.finished_at || run.started_at || ''))}`;
        } else {
            heading = `\u274c Update failed ${escapeHtml(formatRelativeTime(run.finished_at || run.started_at || ''))}`;
        }

        const notes = [];
        if (run.requested_by) notes.push(`requested from ${escapeHtml(run.requested_by)}`);
        if (run.command) notes.push(`ran <code>${escapeHtml(run.command)}</code>`);
        if (run.error) notes.push(escapeHtml(run.error));
        if (abandoned) {
            notes.push('the client stopped reporting before the run finished \u2014 check the host\u2019s journal');
        }

        // Open while the run is going — the point of streaming is seeing it —
        // and on a failure, where the output is the answer. Collapsed after a
        // success, where it is a few hundred package names. A pane the reader
        // has already opened or closed keeps their choice, so a run finishing
        // (or any other host checking in) does not shut it under them.
        const text = runOutputText(hostname, run);
        const remembered = outputViewState.get(hostname);
        const open = remembered && remembered.id === run.id
            ? remembered.open
            : active || run.status === "failed";
        const output = (text || active)
            ? `<details class="update-run-output"${open ? " open" : ""}>
                    <summary>Command output${active ? ' <span class="update-run-live">live</span>' : ''}</summary>
                    <pre>${text
                        ? escapeHtml(text)
                        : '<span class="update-run-waiting">waiting for output\u2026</span>'}</pre>
                </details>`
            : '';

        return `<div class="update-run update-run-${escapeHtml(active ? 'running' : run.status || 'unknown')}">
                <h4>${heading}</h4>
                ${notes.length ? `<p>${notes.join(' \u00b7 ')}</p>` : ''}
                ${output}
            </div>`;
    }

    // The record of the last dashboard-triggered reboot, in the expanded
    // details. It borrows the update-run block's styling: the colours mean the
    // same things.
    function rebootHTML(reboot) {
        if (!reboot) return '';

        const overdue = isRebootOverdue(reboot);
        const active = isRebooting(reboot) && !overdue;
        let heading;
        let tone;
        if (active) {
            heading = `\u23fb Rebooting &mdash; requested ${escapeHtml(formatRelativeTime(reboot.requested_at || ''))}`;
            tone = 'running';
        } else if (overdue) {
            heading = `\u2753 Reboot requested ${escapeHtml(formatRelativeTime(reboot.requested_at || ''))} &mdash; the host has not checked in since`;
            tone = 'unknown';
        } else if (reboot.status === "rebooted") {
            heading = `\u2705 Rebooted &mdash; back ${escapeHtml(formatRelativeTime(reboot.finished_at || reboot.requested_at || ''))}`;
            tone = 'succeeded';
        } else {
            heading = `\u274c Reboot failed ${escapeHtml(formatRelativeTime(reboot.finished_at || reboot.requested_at || ''))}`;
            tone = 'failed';
        }

        const notes = [];
        if (reboot.requested_by) notes.push(`requested from ${escapeHtml(reboot.requested_by)}`);
        if (reboot.command) notes.push(`ran <code>${escapeHtml(reboot.command)}</code>`);
        if (reboot.error) notes.push(escapeHtml(reboot.error));
        if (overdue) {
            notes.push('it may still be coming up, or it may need a look at the console');
        }

        return `<div class="update-run reboot-record update-run-${tone}">
                <h4>${heading}</h4>
                ${notes.length ? `<p>${notes.join(' \u00b7 ')}</p>` : ''}
            </div>`;
    }

    // The actions on a host's row: check in, update, reboot. They sit on the
    // row rather than in the expanded panel so the common jobs take one click
    // (two for a reboot) without opening anything.
    //
    // A control the server does not offer at all is left out, so a fleet
    // without remote updates never sees the column grow. A control the server
    // offers but this host has not opted into is drawn disabled with the
    // reason on hover: the column then reads down the table as "which hosts
    // can I do this to", and every row keeps its buttons in the same place.
    function rowActionsHTML(system) {
        const hostname = system.hostname;
        const host = escapeHtml(hostname);

        const checkInPending = pendingCheckIns.has(hostname);
        const checkIn = `<button class="check-in-btn" data-hostname="${host}"${checkInPending ? ' disabled' : ''}>` +
            `${checkInPending ? CHECK_IN_WAITING_LABEL : CHECK_IN_LABEL}</button>`;

        const runActive = isRunActive(system.last_update_run);
        let update = '';
        if (features.remote_updates) {
            const starting = startingUpdates.has(hostname);
            let blocker = '';
            if (!system.remote_updates_enabled) {
                blocker = 'Remote updates are turned off on this host';
            } else if (!starting && !runActive && !system.updates_available && !system.update_status_unknown) {
                blocker = 'Nothing to install';
            }
            const label = runActive ? RUN_UPDATE_RUNNING_LABEL : starting ? RUN_UPDATE_STARTING_LABEL : RUN_UPDATE_LABEL;
            update = `<button class="run-update-btn" data-hostname="${host}"` +
                `${blocker || runActive || starting ? ' disabled' : ''}` +
                `${blocker ? ` data-tooltip="${escapeHtml(blocker)}" data-tooltip-align="right"` : ''}>${label}</button>`;
        }

        return `<div class="row-actions">${checkIn}${update}${rebootControlsHTML(system, runActive)}</div>`;
    }

    // The reboot control is a checkbox and a button: the checkbox is the
    // confirmation, so a stray click on the button does nothing until the
    // operator has ticked it. Both are disabled unless the host has opted in,
    // reports a reboot pending, and nothing else is going on.
    function rebootControlsHTML(system, runActive) {
        if (!features.remote_reboot) return '';

        const hostname = system.hostname;
        const reboot = system.last_reboot;
        const pending = pendingReboots.has(hostname);
        let blocker = '';
        if (!system.remote_reboot_enabled) {
            blocker = 'Remote reboot is turned off on this host';
        } else if (pending || (isRebooting(reboot) && !isRebootOverdue(reboot))) {
            blocker = 'A reboot is in progress';
        } else if (runActive) {
            blocker = 'An update is running; reboot when it has finished';
        } else if (!system.reboot_required) {
            blocker = 'No reboot pending';
        }
        const armed = !blocker && armedReboots.has(hostname);
        const label = pending || (isRebooting(reboot) && !isRebootOverdue(reboot)) ? REBOOT_WAITING_LABEL : REBOOT_LABEL;
        const tip = blocker
            ? ` data-tooltip="${escapeHtml(blocker)}" data-tooltip-align="right"`
            : ` data-tooltip="Tick to confirm, then Reboot" data-tooltip-align="right"`;

        return `<span class="reboot-controls${blocker ? ' blocked' : ''}${armed ? ' armed' : ''}"${tip}>
                    <input type="checkbox" class="reboot-arm-checkbox" data-hostname="${escapeHtml(hostname)}"
                           aria-label="Confirm reboot of ${escapeHtml(hostname)}"${armed ? ' checked' : ''}${blocker ? ' disabled' : ''}>
                    <button class="reboot-btn" data-hostname="${escapeHtml(hostname)}"${armed ? '' : ' disabled'}>${label}</button>
                </span>`;
    }

    // The checkbox arms the button, and only for this render: the armed set
    // is what carries it across the next one.
    function handleRebootArm(checkbox) {
        const hostname = checkbox.dataset.hostname;
        if (!hostname) return;
        if (checkbox.checked) {
            armedReboots.add(hostname);
        } else {
            armedReboots.delete(hostname);
        }
        const controls = checkbox.closest('.reboot-controls');
        controls.classList.toggle('armed', checkbox.checked);
        const button = controls.querySelector('.reboot-btn');
        if (button) button.disabled = !checkbox.checked;
    }

    // Ask a host to reboot. The response says the host accepted, which is the
    // last thing it says before going down; its record follows over the
    // WebSocket, and its next check-in is what says it came back.
    function handleReboot(button) {
        const hostname = button.dataset.hostname;
        if (!hostname) {
            console.error("No hostname found for reboot button");
            return;
        }
        if (!armedReboots.has(hostname)) {
            // The button should have been disabled; treat it as if it were.
            return;
        }

        const system = systemsData.find((s) => s && s.hostname === hostname);
        const previousId = (system && system.last_reboot && system.last_reboot.id) || '';
        markRebootPending(hostname, previousId);
        armedReboots.delete(hostname);
        button.disabled = true;
        button.textContent = REBOOT_WAITING_LABEL;
        const checkbox = button.closest('.reboot-controls').querySelector('.reboot-arm-checkbox');
        if (checkbox) {
            checkbox.checked = false;
            checkbox.disabled = true;
        }

        fetch(`/api/systems/${encodeURIComponent(hostname)}/reboot`, { method: 'POST' })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Reboot request failed: ${response.status}`);
                }
                console.log("Reboot requested", body);
            })
            .catch((error) => {
                clearRebootPending(hostname);
                console.error(`Failed to reboot ${hostname}:`, error);
                // Disarmed on purpose: a retry should take a fresh tick, not a
                // second click.
                renderSystems(Array.from(systemsData));
                alert(`Could not reboot ${hostname}:\n\n${error.message}`);
            });
    }

    // previousId is the reboot record the page knew when the button was
    // pressed; a record with a different id is the one we asked for.
    function markRebootPending(hostname, previousId) {
        clearRebootPending(hostname);
        const timer = setTimeout(() => {
            pendingReboots.delete(hostname);
            renderSystems(Array.from(systemsData));
        }, REBOOT_PENDING_TIMEOUT_MS);
        pendingReboots.set(hostname, { previousId, timer });
    }

    function clearRebootPending(hostname) {
        const pending = pendingReboots.get(hostname);
        if (!pending) return;
        clearTimeout(pending.timer);
        pendingReboots.delete(hostname);
    }

    function resolvePendingReboot(system) {
        const pending = pendingReboots.get(system.hostname);
        if (!pending) return;
        const reboot = system.last_reboot;
        if (!reboot || reboot.id === pending.previousId) return;
        clearRebootPending(system.hostname);
    }

    // Ask a host to install its pending updates. The response only says the host
    // took the job; the run itself reports back over NATS and reaches this page
    // as an ordinary system update, which re-renders the panel.
    //
    // No confirmation dialog: the button says what it does, is only lit on a
    // host that has opted in and has something to install, and a second "are
    // you sure?" only trains people to dismiss it.
    function handleRunUpdate(button) {
        const hostname = button.dataset.hostname;
        if (!hostname) {
            console.error("No hostname found for update button");
            return;
        }

        // Held until the run record arrives (see the WebSocket handler) or,
        // failing that, for long enough that a host that never started is
        // offered again rather than stuck at "Starting…".
        startingUpdates.add(hostname);
        button.disabled = true;
        button.textContent = RUN_UPDATE_STARTING_LABEL;

        fetch(`/api/systems/${encodeURIComponent(hostname)}/update`, { method: 'POST' })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Update request failed: ${response.status}`);
                }
                console.log("Update started", body);
                setTimeout(() => {
                    if (startingUpdates.delete(hostname)) renderSystems(Array.from(systemsData));
                }, 30000);
            })
            .catch((error) => {
                console.error(`Failed to start update on ${hostname}:`, error);
                startingUpdates.delete(hostname);
                renderSystems(Array.from(systemsData));
                alert(`Could not start the update on ${hostname}:\n\n${error.message}`);
            });
    }

    // Ask a host to check in now rather than at its next poll. The response only
    // says the host accepted; the check-in itself arrives over the WebSocket a
    // moment later and re-renders the row, which is what releases the button.
    //
    // No confirmation dialog and no opt-in behind it: this collects and
    // publishes what the host reports anyway, and changes nothing.
    function handleCheckIn(button) {
        const hostname = button.dataset.hostname;
        if (!hostname) {
            console.error("No hostname found for check-in button");
            return;
        }

        const system = systemsData.find((s) => s && s.hostname === hostname);
        markCheckInPending(hostname, (system && system.updates_checked_at) || '');
        button.disabled = true;
        button.textContent = CHECK_IN_WAITING_LABEL;

        fetch(`/api/systems/${encodeURIComponent(hostname)}/checkin`, { method: 'POST' })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Check-in request failed: ${response.status}`);
                }
                console.log("Check-in requested", body);
            })
            .catch((error) => {
                clearCheckInPending(hostname);
                console.error(`Failed to request a check-in on ${hostname}:`, error);
                button.disabled = false;
                button.textContent = CHECK_IN_LABEL;
                if (!button.isConnected) {
                    // A re-render replaced this button while the request was in
                    // flight, so the one the operator is looking at is a
                    // different node and still says "Checking in…".
                    renderSystems(Array.from(systemsData));
                }
                // Last, so the page is already right when the modal clears.
                alert(`Could not ask ${hostname} to check in:\n\n${error.message}`);
            });
    }

    // since is the host's collection time as the page knew it when the button
    // was pressed; anything newer is the check we asked for, or one that landed
    // first and answers the same question.
    function markCheckInPending(hostname, since) {
        clearCheckInPending(hostname);
        const timer = setTimeout(() => {
            // Nothing arrived. The host may have gone away between accepting
            // and publishing, and a control stuck at "Checking in…" is worse
            // than one that lets the operator try again.
            pendingCheckIns.delete(hostname);
            renderSystems(Array.from(systemsData));
        }, CHECK_IN_TIMEOUT_MS);
        pendingCheckIns.set(hostname, { since, timer });
    }

    function clearCheckInPending(hostname) {
        const pending = pendingCheckIns.get(hostname);
        if (!pending) return;
        clearTimeout(pending.timer);
        pendingCheckIns.delete(hostname);
    }

    // Release a pending check-in against a payload that just arrived. It keys on
    // updates_checked_at, which is when the client actually read its package
    // manager: last_seen is stamped by the server on receipt, so it would also
    // move for a check-in whose data was collected before the button was
    // pressed.
    function resolveCheckIn(system) {
        const pending = pendingCheckIns.get(system.hostname);
        if (!pending) return;
        const checked = system.updates_checked_at || '';
        if (pending.since && checked <= pending.since) return;
        clearCheckInPending(system.hostname);
    }

    // Handle system deletion
    function handleDeleteSystem(button) {
        const hostname = button.dataset.hostname;
        if (!hostname) {
            console.error("No hostname found for delete button");
            return;
        }

        // Show confirmation dialog
        const confirmed = confirm(
            `Remove ${hostname} from the dashboard?\n\n` +
            `Its record is deleted. If the host is still running, it comes back the next time it checks in.`
        );

        if (!confirmed) {
            return;
        }

        button.disabled = true;
        button.textContent = "Removing\u2026";

        // Send DELETE request
        fetch(`/api/systems/${encodeURIComponent(hostname)}`, {
            method: 'DELETE',
        })
            .then((response) => {
                if (!response.ok) {
                    return response.text().then(text => {
                        throw new Error(`Failed to delete system: ${response.status} - ${text}`);
                    });
                }
                return response.json();
            })
            .then((data) => {
                console.log("System deleted successfully", data);
                
                // Remove system from local data
                const index = systemsData.findIndex(s => s && s.hostname === hostname);
                if (index !== -1) {
                    systemsData.splice(index, 1);
                }
                
                // Re-render the table (this will remove the deleted system)
                renderSystems(Array.from(systemsData));
            })
            .catch((error) => {
                console.error(`Failed to delete system ${hostname}:`, error);
                alert(`Could not remove ${hostname}:\n\n${error.message}`);
                
                button.disabled = false;
                button.textContent = "Remove host";
            });
    }

    // Put the reader back where they were after the panel was rebuilt, and keep
    // track of where that is from here on. A pane pinned to the bottom follows
    // the stream; one the reader has scrolled up in stays put.
    function restoreOutputView(hostname, detailsContent, run) {
        const details = detailsContent.querySelector('.update-run-output');
        const pre = details && details.querySelector('pre');
        if (!details || !pre || !run) return;

        const previous = outputViewState.get(hostname);
        const state = previous && previous.id === run.id
            ? previous
            // A new run starts open at the bottom: the point of it is watching.
            : { id: run.id, open: details.open, scrollTop: 0, pinned: true };
        state.open = details.open;
        outputViewState.set(hostname, state);

        if (state.pinned) {
            pre.scrollTop = pre.scrollHeight;
        } else {
            pre.scrollTop = state.scrollTop;
        }

        details.addEventListener('toggle', () => {
            state.open = details.open;
        });
        pre.addEventListener('scroll', () => {
            state.scrollTop = pre.scrollTop;
            state.pinned = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
        });
    }

    // Render one host's expanded details from a system payload, whether it
    // came from the API or arrived over the WebSocket.
    function renderSystemDetails(hostname, detailsContent, data) {
        // The host's facts. Uptime, reboot status and last check-in live in
        // the row; the deeper hardware facts live here.
        const infoRows = [
            ['Client', data.client_version ? escapeHtml(data.client_version) : ''],
            ['CPU', data.cpu_model ? escapeHtml(data.cpu_model) : ''],
            ['Cores', data.cpu_cores ? escapeHtml(String(data.cpu_cores)) : ''],
            ['RAM', escapeHtml(formatBytes(data.memory_total_bytes))],
            ['Tailnet', tailnetDetail(data)],
            // updates_checked_at deliberately has no row here. Two
            // timestamps for "when did we last hear about this host"
            // only invite the reader to notice they disagree; Last Seen
            // in the table is the single place that answers it. The
            // check time still drives the ⚠ next to the update badge
            // when the data behind it has gone stale.
        ].filter(([, value]) => value !== '');

        // Surface an incomplete check explicitly: the pending list is a lower
        // bound, not an answer, when a repository was skipped.
        const warnings = data.update_check_warnings || [];
        const warningsHTML = warnings.length
            ? `<div class="update-check-warnings">
                <h4>⚠ Update check was incomplete</h4>
                <ul>${warnings.map((w) => `<li>${escapeHtml(w)}</li>`).join('')}</ul>
            </div>`
            : '';

        const pending = data.pending_updates || [];
        let packagesHTML;
        if (pending.length > 0) {
            packagesHTML = `
                <h4>Pending updates <span class="details-count">${pending.length}</span></h4>
                <div class="updates-scroll">
                    <table class="updates-table">
                        <tbody>
                            ${pending.map((update) => `<tr>
                                <td>${escapeHtml(update.name)}</td>
                                <td>${escapeHtml(update.version || "")}</td>
                                <td>${escapeHtml(update.source)}</td>
                            </tr>`).join("")}
                        </tbody>
                    </table>
                </div>`;
        } else if (data.update_status_unknown) {
            packagesHTML = `
                <h4>Update status unknown</h4>
                <p class="details-note">${warnings.length
                    ? 'The update check ran but could not see everything, so an empty result cannot be trusted as "up to date".'
                    : 'No supported package manager was detected, or its update check failed to run.'}</p>`;
        } else {
            packagesHTML = `
                <h4>Pending updates</h4>
                <p class="details-note">Nothing to install.</p>`;
        }

        // Removing a host is rare and only undone by the host checking in
        // again, so it sits at the foot of the facts rather than with the row
        // actions. A long silence is the usual reason to reach for it, so that
        // is said beside it.
        const staleDays = getStaleDays(data.last_seen);
        const silentNote = isStaleCheckIn(data.last_seen) && staleDays >= 7 && staleDays !== Infinity
            ? `<span class="details-silent">Silent for ${staleDays} days.</span>`
            : '';

        detailsContent.innerHTML = `
            <div class="details-grid">
                <section class="details-packages">
                    ${packagesHTML}
                    ${warningsHTML}
                </section>
                <section class="details-host">
                    <h4>Host</h4>
                    ${infoRows.length
                        ? `<dl class="system-info">${infoRows.map(([label, value]) => `<dt>${label}</dt><dd>${value}</dd>`).join('')}</dl>`
                        : ''}
                </section>
                <section class="details-groups">
                    <h4>Groups</h4>
                    ${groupEditorHTML(hostname)}
                    <div class="details-remove">
                        ${silentNote}
                        <button class="delete-system-btn" data-hostname="${escapeHtml(hostname)}">Remove host</button>
                    </div>
                </section>
            </div>
            ${updateRunHTML(hostname, data.last_update_run)}
            ${rebootHTML(data.last_reboot)}
        `;
        detailsContent.dataset.loaded = "true";
        detailsContent.dataset.loadedTime = Date.now();

        restoreOutputView(hostname, detailsContent, data.last_update_run);
        seedLiveOutput(hostname, data.last_update_run);
    }

    // Render a host's expanded details. The list payload already carries
    // nearly everything the panel shows, so it draws from that at once rather
    // than blanking to "Loading…"; the full record (which adds the client
    // version) is fetched behind it unless the WebSocket just delivered it.
    function loadSystemDetails(hostname, detailsContent) {
        const cached = detailPayloads.get(hostname);
        if (cached && Date.now() - cached.at < 5000) {
            renderSystemDetails(hostname, detailsContent, cached.data);
            return;
        }

        const summary = systemsData.find((s) => s && s.hostname === hostname);
        if (summary) {
            renderSystemDetails(hostname, detailsContent, { ...(cached ? cached.data : {}), ...summary });
        }

        fetch(`/api/systems/${encodeURIComponent(hostname)}`)
            .then((response) => {
                if (!response.ok) {
                    throw new Error(`Failed to fetch system details: ${response.status}`);
                }
                return response.json();
            })
            .then((data) => {
                detailPayloads.set(hostname, { data, at: Date.now() });
                // The table may have been rebuilt while this was in flight;
                // draw into whichever panel is on screen now.
                const current = document.querySelector(`.details-row${hostnameAttr(hostname)} .details-content`);
                if (current) renderSystemDetails(hostname, current, data);
            })
            .catch((error) => {
                console.error(`Failed to fetch system details for ${hostname}:`, error);
                if (summary) return; // the summary is already on screen
                detailsContent.innerHTML = `
                    <div style="padding: 16px; border-left: 3px solid var(--tape-red); background: var(--bg-tertiary); color: var(--accent-red);">
                        <strong>Could not load details for ${escapeHtml(hostname)}</strong>
                        <div style="margin-top: 8px; font-size: 13px; opacity: 0.9;">${escapeHtml(error.message || 'Collapse and expand the row to try again')}</div>
                    </div>
                `;
            });
    }

    function setChevron(chevron, open) {
        chevron.textContent = open ? "▼" : "▶";
        chevron.setAttribute("aria-expanded", open ? "true" : "false");
    }

    function toggleDetails(hostname) {
        const detailsRow = document.querySelector(`.details-row${hostnameAttr(hostname)}`);
        const chevron = document.querySelector(`tr${hostnameAttr(hostname)} .chevron`);
        if (!detailsRow || !chevron) return;
        const detailsContent = detailsRow.querySelector(".details-content");

        if (detailsRow.style.display === "none") {
            detailsRow.style.display = "table-row";
            setChevron(chevron, true);
            expandedSystems.add(hostname);
            if (!detailsContent.dataset.loaded || Date.now() - detailsContent.dataset.loadedTime > 60000) {
                loadSystemDetails(hostname, detailsContent);
            }
        } else {
            detailsRow.style.display = "none";
            setChevron(chevron, false);
            expandedSystems.delete(hostname);
        }
    }

    // One listener for everything in the table, attached once. The rows are
    // rebuilt on every check-in from any host, so per-button listeners would
    // have to be re-attached on every render; delegation does not care.
    systemsTable.addEventListener("click", (event) => {
        const button = event.target.closest("button");
        if (button && !button.disabled) {
            if (button.classList.contains("check-in-btn")) return handleCheckIn(button);
            if (button.classList.contains("run-update-btn")) return handleRunUpdate(button);
            if (button.classList.contains("reboot-btn")) return handleReboot(button);
            if (button.classList.contains("delete-system-btn")) return handleDeleteSystem(button);
        }

        // Anywhere else on a host's row opens or closes its details — the
        // chevron is only the visible hint. Controls and the details panel
        // itself are left alone, and so is a click that ends a text selection:
        // copying an address out of the row should not fold it.
        const row = event.target.closest("tr[data-hostname]:not(.details-row)");
        if (!row) return;
        if (!event.target.closest(".chevron") &&
            event.target.closest("button, input, label, a, .row-actions")) return;
        if (String(window.getSelection && window.getSelection()).length > 0) return;
        toggleDetails(row.dataset.hostname);
    });

    systemsTable.addEventListener("change", (event) => {
        const target = event.target;
        if (target.classList.contains("reboot-arm-checkbox")) handleRebootArm(target);
        else if (target.classList.contains("group-member-checkbox")) handleGroupMemberToggle(target);
    });

    // Add event listeners to table headers for sorting
    document.querySelectorAll("th.sortable").forEach((header) => {
        header.addEventListener("click", () => {
            const column = header.dataset.column;

            // Toggle sort order if the same column is clicked
            if (sortOrder.column === column) {
                sortOrder.ascending = !sortOrder.ascending;
            } else {
                sortOrder.column = column;
                sortOrder.ascending = true;
            }

            // Re-render the systems with updated sorting
            renderSystems(systemsData);
        });
    });

    // Initial fetch and WebSocket connection. Features first, so the first
    // render of an expanded row already knows whether to offer the update
    // button; the fetch is not allowed to hold up the systems list for long.
    initGroupControls();
    Promise.all([fetchFeatures(), fetchGroups()]).finally(fetchSystems);
    initWebSocket();
    
    // Set up periodic update of relative timestamps (every 30 seconds)
    timestampUpdateIntervalId = setInterval(() => {
        updateRelativeTimestamps();
    }, 30000);
});

