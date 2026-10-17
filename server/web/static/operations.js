// The operations log page: what hosts were asked to do and what they said
// back, newest first, for every host or for the one named in ?host=.
(function() {
    const PAGE_SIZE = 100;
    const REFRESH_MS = 30000;

    const rows = document.getElementById('oplog-rows');
    const hostSelect = document.getElementById('oplog-host');
    const status = document.getElementById('oplog-status');
    const moreBtn = document.getElementById('oplog-more');

    let host = new URLSearchParams(location.search).get('host') || '';
    // Seq of the newest and oldest entries on the page: the refresh asks for
    // what is newer than the one, "Show older" for what is older than the other.
    let newestSeq = 0;
    let oldestSeq = 0;

    function escapeHtml(text) {
        return String(text == null ? '' : text)
            .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    }

    // The same format the dashboard's tooltips use, so a time read here can be
    // matched against one read there.
    function formatFullTimestamp(iso) {
        const date = new Date(iso);
        if (isNaN(date)) return iso || '';
        const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
        const pad = (n) => String(n).padStart(2, '0');
        return `${pad(date.getUTCDate())} ${months[date.getUTCMonth()]} ${date.getUTCFullYear()} ` +
            `${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}:${pad(date.getUTCSeconds())} UTC`;
    }

    // The exact time is what a log is for; how long ago it was is on hover.
    function formatAgo(iso) {
        const seconds = Math.floor((Date.now() - Date.parse(iso)) / 1000);
        if (isNaN(seconds)) return '';
        if (seconds < 60) return 'just now';
        const units = [['year', 31536000], ['month', 2592000], ['week', 604800], ['day', 86400], ['hour', 3600], ['minute', 60]];
        for (const [name, size] of units) {
            const n = Math.floor(seconds / size);
            if (n >= 1) return `${n} ${name}${n === 1 ? '' : 's'} ago`;
        }
        return 'just now';
    }

    const ACTIONS = { update: 'Update', reboot: 'Reboot', checkin: 'Check-in' };

    // What a line says happened, and the colour it is said in. A request is
    // described by what became of it; the host's own reports by their event.
    function describe(op) {
        if (op.event === 'requested') {
            switch (op.outcome) {
                case 'accepted': return ['Requested', 'neutral'];
                case 'skipped': return ['Not sent', 'muted'];
                case 'refused': return ['Refused by host', 'bad'];
                case 'unreachable': return ['Host unreachable', 'bad'];
                default: return ['Request failed', 'bad'];
            }
        }
        switch (op.event) {
            case 'running': return ['Started', 'busy'];
            case 'succeeded': return ['Succeeded', 'good'];
            case 'rebooting': return ['Going down', 'busy'];
            case 'rebooted': return ['Back up', 'good'];
            case 'failed': return ['Failed', 'bad'];
            default: return [op.event || 'unknown', 'muted'];
        }
    }

    function rowHTML(op) {
        const [what, tone] = describe(op);
        const detail = [];
        if (op.detail) detail.push(escapeHtml(op.detail));
        if (op.command) detail.push(`<code>${escapeHtml(op.command)}</code>`);
        if (op.group) detail.push(`via group ${escapeHtml(op.group)}`);
        if (op.id) detail.push(`<span class="oplog-id" title="Ties together the lines about one ${escapeHtml(ACTIONS[op.action] || 'operation').toLowerCase()}">${escapeHtml(op.id)}</span>`);
        return `<tr data-seq="${op.seq}">
            <td class="oplog-time" title="${escapeHtml(formatAgo(op.time))}">${escapeHtml(formatFullTimestamp(op.time))}</td>
            <td><a href="?host=${encodeURIComponent(op.hostname)}">${escapeHtml(op.hostname)}</a></td>
            <td>${escapeHtml(ACTIONS[op.action] || op.action)}</td>
            <td><span class="oplog-event oplog-${tone}">${escapeHtml(what)}</span></td>
            <td>${escapeHtml(op.requested_by || '')}</td>
            <td class="oplog-detail">${detail.join(' · ')}</td>
        </tr>`;
    }

    function fetchOps(before) {
        const params = new URLSearchParams({ limit: PAGE_SIZE });
        if (host) params.set('host', host);
        if (before) params.set('before', before);
        return fetch('/api/operations?' + params).then((res) => {
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            return res.json();
        });
    }

    function setEmpty() {
        rows.innerHTML = `<tr class="oplog-empty"><td colspan="6">${host
            ? `Nothing has been asked of ${escapeHtml(host)} yet.`
            : 'Nothing has been asked of any host yet. Updates, reboots and check-ins started from the dashboard will show up here.'}</td></tr>`;
    }

    function load() {
        status.textContent = 'Loading…';
        fetchOps(0).then((ops) => {
            status.textContent = '';
            newestSeq = ops.length ? ops[0].seq : 0;
            oldestSeq = ops.length ? ops[ops.length - 1].seq : 0;
            if (ops.length) {
                rows.innerHTML = ops.map(rowHTML).join('');
            } else {
                setEmpty();
            }
            moreBtn.style.display = ops.length === PAGE_SIZE ? '' : 'none';
        }).catch((err) => {
            status.textContent = `Could not load the log: ${err.message}`;
        });
    }

    function loadOlder() {
        moreBtn.disabled = true;
        fetchOps(oldestSeq).then((ops) => {
            if (ops.length) {
                rows.insertAdjacentHTML('beforeend', ops.map(rowHTML).join(''));
                oldestSeq = ops[ops.length - 1].seq;
            }
            moreBtn.style.display = ops.length === PAGE_SIZE ? '' : 'none';
        }).catch((err) => {
            status.textContent = `Could not load older entries: ${err.message}`;
        }).finally(() => {
            moreBtn.disabled = false;
        });
    }

    // Pick up what happened since the page was drawn. Only the first page is
    // asked for; a burst of more than a page between refreshes is redrawn
    // from scratch rather than leaving a gap.
    function refresh() {
        if (document.hidden) return;
        fetchOps(0).then((ops) => {
            const fresh = ops.filter((op) => op.seq > newestSeq);
            if (!fresh.length) return;
            if (fresh.length === ops.length && newestSeq !== 0) {
                load();
                return;
            }
            if (newestSeq === 0) rows.innerHTML = '';
            rows.insertAdjacentHTML('afterbegin', fresh.map(rowHTML).join(''));
            newestSeq = fresh[0].seq;
            if (!oldestSeq) oldestSeq = fresh[fresh.length - 1].seq;
        }).catch(() => {
            // The next refresh will try again; the page already shows what it had.
        });
    }

    // The host list comes from the systems the server knows, plus the one in
    // the URL if it is not among them: a removed host keeps its history.
    function loadHosts() {
        fetch('/api/systems').then((res) => res.ok ? res.json() : []).then((systems) => {
            const names = new Set((systems || []).map((s) => s.hostname));
            if (host) names.add(host);
            [...names].sort((a, b) => a.localeCompare(b)).forEach((name) => {
                const opt = document.createElement('option');
                opt.value = name;
                opt.textContent = name;
                hostSelect.appendChild(opt);
            });
            hostSelect.value = host;
        }).catch(() => {});
    }

    function showHost(name) {
        host = name;
        hostSelect.value = name;
        const url = name ? `?host=${encodeURIComponent(name)}` : location.pathname;
        history.pushState({ host: name }, '', url);
        load();
    }

    hostSelect.addEventListener('change', () => showHost(hostSelect.value));
    moreBtn.addEventListener('click', loadOlder);

    // A host link filters in place rather than reloading the page.
    rows.addEventListener('click', (e) => {
        const link = e.target.closest('a[href^="?host="]');
        if (!link || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
        e.preventDefault();
        showHost(new URLSearchParams(link.getAttribute('href')).get('host') || '');
    });

    window.addEventListener('popstate', () => {
        host = new URLSearchParams(location.search).get('host') || '';
        hostSelect.value = host;
        load();
    });

    loadHosts();
    load();
    setInterval(refresh, REFRESH_MS);
    document.addEventListener('visibilitychange', refresh);
})();
