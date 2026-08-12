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

    // Helper function to escape HTML
    function escapeHtml(text) {
        if (text == null) return '';
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
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
                    
                    // Validate that update has required fields
                    if (!update || !update.hostname) {
                        console.warn("Invalid WebSocket update received, missing hostname");
                        return;
                    }
                    
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
                    cell.innerHTML = '<span class="stale-indicator" title="Host has not checked in for 4+ hours">⚠️ </span>' + relativeTime;
                } else {
                    cell.textContent = relativeTime;
                }
                cell.title = formatFullTimestamp(timestamp);
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
    function tailnetTooltip(ts) {
        const name = tailnetName(ts);
        const parts = [];
        if (ts.connected) {
            parts.push(name ? `On tailnet ${name}` : 'On a tailnet (name unavailable)');
            if (ts.magic_dns_suffix && ts.magic_dns_suffix !== name) parts.push(ts.magic_dns_suffix);
            if (ts.ip) parts.push(ts.ip);
        } else {
            parts.push(name ? `Not on a tailnet — last seen on ${name}` : 'Not on a tailnet');
            if (ts.last_connected_at) parts.push(`last connected ${formatRelativeTime(ts.last_connected_at)}`);
            const reason = tailnetStateReason(ts.state);
            if (reason) parts.push(reason);
        }
        return parts.join(' · ');
    }

    // Render the tailnet dot that leads a hostname cell.
    //
    // The dot sits in a fixed-width slot so hostnames line up down the column
    // whether or not a given host has one. The slot is only laid out at all
    // when some host in the table reports a tailnet — a fleet that does not use
    // Tailscale gets neither dots nor the space they would occupy. The slot,
    // not the 8px dot, carries the tooltip: it is a much easier hover target.
    function tailnetIndicator(system, reserveSlot) {
        if (!reserveSlot) return '';
        const ts = system && system.tailscale;
        if (!ts) return '<span class="tailnet-slot"></span>';
        const tooltip = tailnetTooltip(ts);
        return `<span class="tailnet-slot" role="img" title="${escapeHtml(tooltip)}"` +
            ` aria-label="${escapeHtml(tooltip)}">` +
            `<span class="tailnet-indicator${ts.connected ? ' connected' : ''}"></span></span>`;
    }

    // Spell out in the expanded details what the dot only hints at, leading
    // with the tailnet name — that is the part a dot cannot convey.
    //
    // The line stays short enough not to wrap in the details grid: the MagicDNS
    // suffix lives in the dot's tooltip rather than here, and the address is
    // held on one line with the name, since a wrapped address reads as a
    // separate fact rather than a detail of the same one.
    function tailnetDetail(system) {
        const ts = system && system.tailscale;
        if (!ts) return '';
        const name = tailnetName(ts);
        let html = `<span class="tailnet-indicator${ts.connected ? ' connected' : ''}"></span>`;
        if (ts.connected) {
            html += escapeHtml(name || 'connected (tailnet name unavailable)');
            if (ts.ip) html += ` <span class="tailnet-address">· ${escapeHtml(ts.ip)}</span>`;
        } else {
            html += escapeHtml(name ? `${name} — not connected` : 'not connected');
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
                            ? `<span class="data-warning-indicator" title="${escapeHtml(warning)}"> ⚠</span>`
                            : staleData
                            ? `<span class="data-warning-indicator" title="Host is checking in, but its update data was collected ${escapeHtml(formatRelativeTime(system.updates_checked_at))}"> ⚠</span>`
                            : '';
                        return `
                    <tr data-hostname="${system.hostname}"${isStale ? ' class="stale-checkin"' : ''}>
                        <td class="chevron-cell"><span class="chevron">▶</span></td>
                        <td>${tailnetIndicator(system, showTailnetSlot)}${escapeHtml(system.hostname)}${system.reboot_required ? ' <span class="reboot-indicator" title="Reboot required">⟳</span>' : ''}</td>
                        <td class="os-cell">${getOSIcon(system.os)} <span class="os-text">${escapeHtml(system.os || '')} ${escapeHtml(system.os_version || '')}</span></td>
                        <td>${escapeHtml(system.architecture || '')}</td>
                        <td>${escapeHtml(system.ip || '')}</td>
                        <td>${system.update_status_unknown ?
                            `<span class="update-badge status-unknown" title="${escapeHtml(warning || 'Package manager not detected - update status unknown')}">
                                Status unknown
                            </span>` :
                            system.updates_available ?
                            `<span class="update-badge update-available${system.pending_updates ? ' priority-' + getUpdatePriority(system.pending_updates) : ''}"
                                   title="Updates available${system.pending_updates ? ': ' + system.pending_updates.map(u => escapeHtml(u.name)).join(', ') : ' - Click for details'}">
                                Updates${system.pending_updates ? ` (${system.pending_updates.length})` : ' (click for details)'}
                                ${system.pending_updates && getUpdatePriority(system.pending_updates) === 'high' ? ' ⚠️' : ''}
                            </span>${badgeNote}` :
                            `<span class="update-badge up-to-date">Up to date</span>${badgeNote}`
                        }</td>
                        <td>${escapeHtml(formatUptime(system.uptime_seconds))}</td>
                        <td class="last-seen-cell" data-timestamp="${escapeHtml(system.last_seen || '')}" title="${formatFullTimestamp(system.last_seen || '')}">
                            ${isStale ? '<span class="stale-indicator" title="Host has not checked in for 4+ hours">⚠️ </span>' : ''}
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
            const detailsRow = document.querySelector(`.details-row[data-hostname="${hostname}"]`);
            const chevron = document.querySelector(`tr[data-hostname="${hostname}"] .chevron`);
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

    // Function to load system details
    function loadSystemDetails(hostname, detailsContent) {
        fetch(`/api/systems/${hostname}`)
            .then((response) => {
                if (!response.ok) {
                    throw new Error(`Failed to fetch system details: ${response.status}`);
                }
                return response.json();
            })
            .then((data) => {
                let detailsHTML = '';
                
                // Prepare delete button HTML (will be placed at the end)
                const isStale = isStaleCheckIn(data.last_seen);
                const staleDays = getStaleDays(data.last_seen);
                const deleteButtonHTML = `
                    <div style="margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--border-color); text-align: right;">
                        ${isStale && staleDays >= 7 ? 
                            '<span style="margin-right: 12px; color: var(--accent-orange); font-size: 13px; font-weight: 500;">⚠️ This system has not checked in for ' + staleDays + ' days</span>' : 
                            ''}
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
                    // When the package manager was actually queried, as opposed
                    // to Last Seen in the table, which is when the server last
                    // heard from the host.
                    ['Update data collected', data.updates_checked_at
                        ? `<span title="${escapeHtml(formatFullTimestamp(data.updates_checked_at))}">${escapeHtml(formatRelativeTime(data.updates_checked_at))}</span>`
                        : ''],
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
        const detailsRow = document.querySelector(`.details-row[data-hostname="${hostname}"]`);
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
            const chevron = document.querySelector(`tr[data-hostname="${hostname}"] .chevron`);
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
            const chevron = document.querySelector(`tr[data-hostname="${hostname}"] .chevron`);
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

    // Initial fetch and WebSocket connection
    fetchSystems();
    initWebSocket();
    
    // Set up periodic update of relative timestamps (every 30 seconds)
    timestampUpdateIntervalId = setInterval(() => {
        updateRelativeTimestamps();
    }, 30000);
});

