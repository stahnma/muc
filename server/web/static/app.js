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
    let features = { remote_updates: false };

    const RUN_UPDATE_LABEL = "\u2b07\ufe0f Run updates now";

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
                features = Object.assign({ remote_updates: false }, data || {});
            })
            .catch((error) => {
                console.warn("Could not read server features; assuming none:", error);
            });
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

        // Show message if no systems exist yet
        if (systems.length === 0) {
            systemsTable.innerHTML = `
                <tr>
                    <td colspan="8" style="text-align: center; padding: 3rem; color: var(--text-secondary);">
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
                        <td class="chevron-cell"><span class="chevron">▶</span></td>
                        <td>${tailnetIndicator(system, showTailnetSlot)}${escapeHtml(system.hostname)}${system.reboot_required ? ' ' + tooltipIcon('⟳', 'reboot-indicator', 'Reboot required') : ''}</td>
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
                    </tr>
                    <tr class="details-row" data-hostname="${escapeHtml(system.hostname)}" style="display: none;">
                        <td colspan="8">
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
                    <td colspan="8" style="text-align: center; padding: 3rem; color: var(--accent-red);">
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
                chevron.textContent = "▼";
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

    // Ask a host to install its pending updates. The response only says the host
    // took the job; the run itself reports back over NATS and reaches this page
    // as an ordinary system update, which re-renders the panel.
    //
    // No confirmation dialog: reaching this button already takes expanding the
    // host's row and clicking a control that says what it does, so a second
    // "are you sure?" only trains people to dismiss it.
    function handleRunUpdate(event) {
        const button = event.currentTarget;
        const hostname = button.dataset.hostname;
        if (!hostname) {
            console.error("No hostname found for update button");
            return;
        }

        button.disabled = true;
        button.textContent = "Starting\u2026";

        fetch(`/api/systems/${encodeURIComponent(hostname)}/update`, { method: 'POST' })
            .then((response) => response.json().catch(() => ({})).then((body) => ({ response, body })))
            .then(({ response, body }) => {
                if (!response.ok) {
                    throw new Error(body.error || `Update request failed: ${response.status}`);
                }
                console.log("Update started", body);
                button.textContent = "\u23f3 Update running";
            })
            .catch((error) => {
                console.error(`Failed to start update on ${hostname}:`, error);
                alert(`Could not start the update on ${hostname}:\n\n${error.message}`);
                button.disabled = false;
                button.textContent = RUN_UPDATE_LABEL;
            });
    }

    // Handle system deletion
    function handleDeleteSystem(event) {
        const hostname = event.target.dataset.hostname;
        if (!hostname) {
            console.error("No hostname found for delete button");
            return;
        }

        // Show confirmation dialog
        const confirmed = confirm(
            `Are you sure you want to delete "${hostname}"?\n\n` +
            `This action cannot be undone. The system will be removed from the database.\n\n` +
            `If the system is still running, it will reappear when it checks in again.`
        );

        if (!confirmed) {
            return;
        }

        // Disable button during deletion
        event.target.disabled = true;
        event.target.textContent = "Deleting...";

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
                alert(`Failed to delete system: ${error.message}`);
                
                // Re-enable button
                event.target.disabled = false;
                event.target.textContent = "🗑️ Delete System";
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
        let detailsHTML = '';
        
        // Prepare the actions footer (placed at the end): the record of
        // the last update run, then the buttons that act on this host.
        const isStale = isStaleCheckIn(data.last_seen);
        const staleDays = getStaleDays(data.last_seen);
        const runActive = isRunActive(data.last_update_run);
        // Both sides must have opted in: the server offers the feature,
        // and the host reports that it is listening for the command.
        const canRunUpdates = features.remote_updates && data.remote_updates_enabled;
        const runUpdateButtonHTML = canRunUpdates
            ? `<button class="run-update-btn" data-hostname="${escapeHtml(hostname)}"${runActive ? ' disabled' : ''}>
                    ${runActive ? '⏳ Update running' : RUN_UPDATE_LABEL}
                </button>`
            : '';
        const deleteButtonHTML = `
            ${updateRunHTML(hostname, data.last_update_run)}
            <div style="margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--border-color); text-align: right;">
                ${isStale && staleDays >= 7 ? 
                    '<span style="margin-right: 12px; color: var(--accent-orange); font-size: 13px; font-weight: 500;">⚠️ This system has not checked in for ' + staleDays + ' days</span>' : 
                    ''}
                ${runUpdateButtonHTML}
                <button class="delete-system-btn" data-hostname="${escapeHtml(hostname)}">
                    🗑️ Delete System
                </button>
            </div>
        `;
        
        // System information block shown in the expanded per-host details.
        // Uptime and reboot status live in the main table; the deeper
        // hardware facts live here.
        const infoRows = [
            ['Client version', data.client_version ? escapeHtml(data.client_version) : ''],
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

        // Surface an incomplete check explicitly: the pending list below
        // is a lower bound, not an answer, when a repository was skipped.
        const warnings = data.update_check_warnings || [];
        const warningsHTML = warnings.length
            ? `<div class="update-check-warnings">
                <h4>⚠ Update check was incomplete</h4>
                <ul>${warnings.map((w) => `<li>${escapeHtml(w)}</li>`).join('')}</ul>
            </div>`
            : '';
        const systemInfoHTML = infoRows.length
            ? `<div class="system-info">
                <h4>System information</h4>
                <dl>
                    ${infoRows.map(([label, value]) => `<dt>${label}</dt><dd>${value}</dd>`).join('')}
                </dl>
            </div>`
            : '';

        if (data.pending_updates && data.pending_updates.length > 0) {
            const updatesList = data.pending_updates
                .map(
                    (update) =>
                        `<tr>
                            <td>${escapeHtml(update.name)}</td>
                            <td>${escapeHtml(update.version || "N/A")}</td>
                            <td>${escapeHtml(update.source)}</td>
                        </tr>`
                )
                .join("");
            detailsHTML = `
                <h3>Pending Updates for ${escapeHtml(data.hostname)}</h3>
                ${systemInfoHTML}
                ${warningsHTML}
                <table class="updates-table">
                    <thead>
                        <tr>
                            <th>Package</th>
                            <th>Version</th>
                            <th>Source</th>
                        </tr>
                    </thead>
                    <tbody>
                        ${updatesList}
                    </tbody>
                </table>
                ${deleteButtonHTML}
            `;
        } else if (data.update_status_unknown) {
            detailsHTML = `
                <h3>Update status unknown for ${escapeHtml(data.hostname)}</h3>
                ${systemInfoHTML}
                ${warningsHTML}
                <p>${warnings.length
                    ? 'The update check ran but could not see everything, so an empty result cannot be trusted as "up to date".'
                    : 'No supported package manager was detected, or its update check failed to run. The update status cannot be determined.'}</p>
                ${deleteButtonHTML}
            `;
        } else {
            detailsHTML = `
                <h3>No pending updates for ${escapeHtml(data.hostname)}</h3>
                ${systemInfoHTML}
                ${warningsHTML}
                ${deleteButtonHTML}
            `;
        }
        
        detailsContent.innerHTML = detailsHTML;
        detailsContent.dataset.loaded = "true";
        detailsContent.dataset.loadedTime = Date.now();
        
        // Attach delete button event listener
        const deleteBtn = detailsContent.querySelector('.delete-system-btn');
        if (deleteBtn) {
            deleteBtn.addEventListener('click', handleDeleteSystem);
        }

        // Attach run-update button event listener
        const runUpdateBtn = detailsContent.querySelector('.run-update-btn');
        if (runUpdateBtn) {
            runUpdateBtn.addEventListener('click', handleRunUpdate);
        }

        restoreOutputView(hostname, detailsContent, data.last_update_run);
        seedLiveOutput(hostname, data.last_update_run);
    }

    // Function to load system details
    function loadSystemDetails(hostname, detailsContent) {
        // The WebSocket payload that prompted this re-render carries the same
        // fields the API would return, so render from it rather than blanking
        // the panel to "Loading…" and asking for it again.
        const cached = detailPayloads.get(hostname);
        if (cached && Date.now() - cached.at < 5000) {
            renderSystemDetails(hostname, detailsContent, cached.data);
            return;
        }

        fetch(`/api/systems/${encodeURIComponent(hostname)}`)
            .then((response) => {
                if (!response.ok) {
                    throw new Error(`Failed to fetch system details: ${response.status}`);
                }
                return response.json();
            })
            .then((data) => {
                renderSystemDetails(hostname, detailsContent, data);
            })
            .catch((error) => {
                console.error(`Failed to fetch system details for ${hostname}:`, error);
                detailsContent.innerHTML = `
                    <div style="padding: 16px; background: rgba(248, 81, 73, 0.1); border: 1px solid var(--accent-red); border-radius: 6px; color: var(--accent-red);">
                        <strong>Error loading details</strong>
                        <div style="margin-top: 8px; font-size: 13px; opacity: 0.9;">${escapeHtml(error.message || 'Please try again')}</div>
                    </div>
                `;
            });
    }

    // Handle row toggle for details
    systemsTable.addEventListener("click", (event) => {
        const chevron = event.target.closest(".chevron");
        if (!chevron) return;

        const row = chevron.closest("tr");
        const hostname = row.dataset.hostname;
        const detailsRow = document.querySelector(`.details-row${hostnameAttr(hostname)}`);
        const detailsContent = detailsRow.querySelector(".details-content");

        // Toggle visibility
        if (detailsRow.style.display === "none") {
            detailsRow.style.display = "table-row";
            chevron.textContent = "▼";
            expandedSystems.add(hostname); // Track as manually expanded

            // Fetch details only if not already loaded or if data is stale
            if (!detailsContent.dataset.loaded || Date.now() - detailsContent.dataset.loadedTime > 60000) {
                loadSystemDetails(hostname, detailsContent);
            }
        } else {
            detailsRow.style.display = "none";
            chevron.textContent = "▶";
            expandedSystems.delete(hostname); // Remove from expanded set
        }
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

    // Expand all systems
    function expandAll() {
        document.querySelectorAll('.details-row').forEach(detailsRow => {
            const hostname = detailsRow.dataset.hostname;
            const chevron = document.querySelector(`tr${hostnameAttr(hostname)} .chevron`);
            if (chevron && detailsRow.style.display === "none") {
                detailsRow.style.display = "table-row";
                chevron.textContent = "▼";
                expandedSystems.add(hostname);
                const detailsContent = detailsRow.querySelector(".details-content");
                if (!detailsContent.dataset.loaded || Date.now() - detailsContent.dataset.loadedTime > 60000) {
                    loadSystemDetails(hostname, detailsContent);
                }
            }
        });
    }

    // Collapse all systems
    function collapseAll() {
        document.querySelectorAll('.details-row').forEach(detailsRow => {
            const hostname = detailsRow.dataset.hostname;
            const chevron = document.querySelector(`tr${hostnameAttr(hostname)} .chevron`);
            if (chevron && detailsRow.style.display !== "none") {
                detailsRow.style.display = "none";
                chevron.textContent = "▶";
                expandedSystems.delete(hostname);
            }
        });
    }

    // Add event listeners for expand/collapse all buttons
    const expandAllBtn = document.getElementById("expand-all-btn");
    const collapseAllBtn = document.getElementById("collapse-all-btn");
    
    if (expandAllBtn) {
        expandAllBtn.addEventListener("click", expandAll);
    }
    
    if (collapseAllBtn) {
        collapseAllBtn.addEventListener("click", collapseAll);
    }

    // Initial fetch and WebSocket connection. Features first, so the first
    // render of an expanded row already knows whether to offer the update
    // button; the fetch is not allowed to hold up the systems list for long.
    fetchFeatures().finally(fetchSystems);
    initWebSocket();
    
    // Set up periodic update of relative timestamps (every 30 seconds)
    timestampUpdateIntervalId = setInterval(() => {
        updateRelativeTimestamps();
    }, 30000);
});

