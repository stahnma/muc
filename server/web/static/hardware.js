// The hardware page: every host side by side, ranked by what its benchmark
// measured, with a scatter of single- against multi-thread throughput so fast
// cores and many cores can be told apart at a glance.
(function() {
    const REFRESH_MS = 60000;
    // After a "Run now", poll quickly until the result lands. A run takes
    // about six seconds; give a slow board and a busy server plenty more.
    const FAST_POLL_MS = 3000;
    const FAST_POLL_FOR_MS = 60000;

    const rowsEl = document.getElementById('hw-rows');
    const statusEl = document.getElementById('hw-status');
    const chartEl = document.getElementById('hw-chart');
    const legendEl = document.getElementById('hw-legend');
    const unmeasuredEl = document.getElementById('hw-unmeasured');
    const tooltip = document.getElementById('hw-tooltip');
    const rankButtons = document.querySelectorAll('.hw-rank button');

    let hosts = [];
    let rank = 'multi';
    try {
        rank = localStorage.getItem('muc-hw-rank') || rank;
    } catch (e) { /* storage blocked; rank by the default */ }
    // Hosts whose benchmark was just asked for, by hostname, with the id of
    // the request and when it was made. The row shows them as running until a
    // result with that id arrives or the wait runs out.
    const pending = new Map();
    let fastPoll = null;

    function escapeHtml(text) {
        return String(text == null ? '' : text)
            .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    }

    // The same format the dashboard's tooltips use.
    function formatFullTimestamp(iso) {
        const date = new Date(iso);
        if (isNaN(date)) return iso || '';
        const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
        const pad = (n) => String(n).padStart(2, '0');
        return `${pad(date.getUTCDate())} ${months[date.getUTCMonth()]} ${date.getUTCFullYear()} ` +
            `${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}:${pad(date.getUTCSeconds())} UTC`;
    }

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

    function formatBytes(bytes) {
        if (!bytes) return '';
        const gib = bytes / 2 ** 30;
        if (gib >= 1000) return `${(gib / 1024).toFixed(1)} TiB`;
        if (gib >= 100) return `${Math.round(gib)} GiB`;
        return `${gib.toFixed(1)} GiB`;
    }

    function formatMBps(v) {
        return v >= 100 ? v.toFixed(0) : v.toFixed(1);
    }

    // Model strings carry marketing the comparison does not need: the
    // integrated GPU, trademark marks, and Intel's base clock, which the
    // max-clock line below says better.
    function cleanModel(model) {
        return (model || 'Unknown CPU')
            .replace(/\((R|TM)\)/gi, '')
            .replace(/\s+with Radeon.*$/i, '')
            .replace(/\s+CPU\s+@.*$/i, '')
            .replace(/\s+[\w]+-Core Processor$/i, '')
            .replace(/\s+Processor$/i, '')
            .replace(/\s+/g, ' ')
            .trim();
    }

    const DISK_MEDIA = { nvme: 'NVMe', ssd: 'SSD', hdd: 'HDD', flash: 'SD/eMMC', virtual: 'Virtual disk' };
    // Faster media ranks higher when ranking by disk; size breaks ties.
    const DISK_RANK = { nvme: 5, ssd: 4, virtual: 3, flash: 2, hdd: 1 };

    // Architecture is the chart's one categorical encoding. The order is
    // fixed so a host keeps its colour whatever else is on the page.
    const ARCHES = [
        { key: 'x86', label: 'x86_64', match: (a) => a === 'x86_64' || a === 'amd64' },
        { key: 'arm', label: 'aarch64', match: (a) => a === 'aarch64' || a === 'arm64' },
    ];
    function archKey(arch) {
        const found = ARCHES.find((a) => a.match(arch));
        return found ? found.key : 'other';
    }

    function bench(h) {
        return h.benchmark && h.benchmark.multi_thread_mbps > 0 ? h.benchmark : null;
    }

    const RANKERS = {
        multi: (h) => bench(h) ? bench(h).multi_thread_mbps : null,
        single: (h) => bench(h) ? bench(h).single_thread_mbps : null,
        memory: (h) => h.memory_total_bytes || null,
        disk: (h) => h.disk_total_bytes ? (DISK_RANK[h.disk_media] || 0) * 1e15 + h.disk_total_bytes : null,
    };

    function sorted() {
        const key = RANKERS[rank] || RANKERS.multi;
        return [...hosts].sort((a, b) => {
            const ka = key(a), kb = key(b);
            if (ka == null && kb == null) return a.hostname.localeCompare(b.hostname);
            if (ka == null) return 1;
            if (kb == null) return -1;
            return kb - ka || a.hostname.localeCompare(b.hostname);
        });
    }

    // Which other hosts share this one's CPU model, so a reader can see how
    // much of a difference is the chip and how much is what the host gets.
    function siblings(h) {
        if (!h.cpu_model) return [];
        return hosts.filter((o) => o !== h && o.cpu_model === h.cpu_model).map((o) => o.hostname).sort();
    }

    function bar(value, best, active) {
        if (value == null) return '<span class="hw-none">—</span>';
        const pct = best > 0 ? Math.max(2, Math.round((value / best) * 100)) : 0;
        return `<div class="hw-score${active ? ' hw-active' : ''}" title="${pct}% of the best on this page">
            <span class="hw-score-value">${formatMBps(value)}</span>
            <span class="hw-bar"><span class="hw-bar-fill" style="width: ${pct}%"></span></span>
        </div>`;
    }

    function cpuCell(h) {
        const facts = [];
        if (h.cpu_physical_cores) facts.push(`${h.cpu_physical_cores} cores`);
        if (h.cpu_cores) facts.push(`${h.cpu_cores} threads`);
        if (h.cpu_max_mhz) facts.push(`${(h.cpu_max_mhz / 1000).toFixed(1)} GHz`);
        const sib = siblings(h);
        return `<div class="hw-cpu" title="${escapeHtml(h.cpu_model)}">${escapeHtml(cleanModel(h.cpu_model))}</div>
            <div class="hw-sub">${escapeHtml(facts.join(' · '))}</div>
            ${sib.length ? `<div class="hw-sub hw-sibling">Same chip as ${escapeHtml(sib.join(', '))}</div>` : ''}`;
    }

    function memoryCell(h, best) {
        if (!h.memory_total_bytes) return '<span class="hw-none">—</span>';
        const pct = best > 0 ? Math.max(2, Math.round((h.memory_total_bytes / best) * 100)) : 0;
        return `<div class="hw-score${rank === 'memory' ? ' hw-active' : ''}">
                <span class="hw-score-value">${formatBytes(h.memory_total_bytes)}</span>
                <span class="hw-bar"><span class="hw-bar-fill" style="width: ${pct}%"></span></span>
            </div>
            ${h.memory_available_bytes ? `<div class="hw-sub">${formatBytes(h.memory_available_bytes)} available</div>` : ''}`;
    }

    function diskCell(h) {
        if (!h.disk_total_bytes) return '<span class="hw-none">—</span>';
        const media = DISK_MEDIA[h.disk_media];
        return `<div class="${rank === 'disk' ? 'hw-active-text' : ''}">${media ? `<span class="hw-media hw-media-${escapeHtml(h.disk_media)}">${media}</span> ` : ''}${formatBytes(h.disk_total_bytes)}</div>
            ${h.disk_free_bytes ? `<div class="hw-sub">${formatBytes(h.disk_free_bytes)} free</div>` : ''}`;
    }

    function measuredCell(h) {
        if (pending.has(h.hostname)) {
            return '<span class="hw-running">Running…</span>';
        }
        if (!h.benchmark_supported) {
            const when = bench(h) ? `<div class="hw-sub" title="${escapeHtml(formatFullTimestamp(h.benchmark.finished_at))}">${escapeHtml(formatAgo(h.benchmark.finished_at))}</div>` : '';
            return `${when}<div class="hw-sub" title="Upgrade muc-client on this host to benchmark it">Client too old</div>`;
        }
        const b = bench(h);
        const when = b
            ? `<div class="hw-when" title="${escapeHtml(formatFullTimestamp(b.finished_at))}">${escapeHtml(formatAgo(b.finished_at))}</div>`
            : '<div class="hw-when hw-none">Not yet</div>';
        return `${when}<button type="button" class="hw-run" data-host="${escapeHtml(h.hostname)}">${b ? 'Re-run' : 'Run now'}</button>`;
    }

    function renderTable() {
        const list = sorted();
        if (!list.length) {
            rowsEl.innerHTML = '<tr class="oplog-empty"><td colspan="8">No hosts have checked in yet.</td></tr>';
            return;
        }
        const best = (f) => Math.max(0, ...hosts.map(f).filter((v) => v != null));
        const bestMulti = best(RANKERS.multi);
        const bestSingle = best(RANKERS.single);
        const bestMem = best((h) => h.memory_total_bytes || null);

        let n = 0;
        rowsEl.innerHTML = list.map((h) => {
            const b = bench(h);
            const ranked = RANKERS[rank](h) != null;
            const tags = [];
            if (h.virtualization) tags.push(`<span class="hw-tag" title="A guest of ${escapeHtml(h.virtualization)}">VM · ${escapeHtml(h.virtualization)}</span>`);
            return `<tr>
                <td class="hw-rank-col">${ranked ? ++n : ''}</td>
                <td class="hw-host">
                    <a href="/hosts/${encodeURIComponent(h.hostname)}">${escapeHtml(h.hostname)}</a>
                    <div class="hw-sub"><span class="hw-arch-dot hw-arch-${archKey(h.architecture)}" aria-hidden="true"></span>${escapeHtml(h.architecture || '')} ${tags.join(' ')}</div>
                </td>
                <td>${cpuCell(h)}</td>
                <td>${bar(b ? b.multi_thread_mbps : null, bestMulti, rank === 'multi')}
                    ${b ? `<div class="hw-sub">on ${b.threads} thread${b.threads === 1 ? '' : 's'}</div>` : ''}</td>
                <td>${bar(b ? b.single_thread_mbps : null, bestSingle, rank === 'single')}</td>
                <td>${memoryCell(h, bestMem)}</td>
                <td class="hw-disk">${diskCell(h)}</td>
                <td class="hw-measured">${measuredCell(h)}</td>
            </tr>`;
        }).join('');
    }

    // ---- The scatter ---------------------------------------------------

    // A round axis maximum, so gridlines land on numbers a person would pick.
    function niceMax(v) {
        if (v <= 0) return 1;
        const mag = 10 ** Math.floor(Math.log10(v));
        for (const m of [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) {
            if (m * mag >= v * 1.08) return m * mag;
        }
        return 10 * mag;
    }

    function renderLegend(present) {
        const items = ARCHES.filter((a) => present.has(a.key)).map((a) => [a.key, a.label]);
        if (present.has('other')) items.push(['other', 'Other']);
        legendEl.innerHTML = items.length > 1
            ? items.map(([k, label]) => `<li><span class="hw-arch-dot hw-arch-${k}" aria-hidden="true"></span>${label}</li>`).join('')
            : '';
    }

    function renderChart() {
        const measured = hosts.filter(bench);
        const unmeasured = hosts.filter((h) => !bench(h)).map((h) => h.hostname).sort();
        unmeasuredEl.textContent = unmeasured.length
            ? `Not measured yet: ${unmeasured.join(', ')}.`
            : '';

        if (!measured.length) {
            chartEl.innerHTML = '<p class="hw-chart-empty">No benchmarks yet. Hosts are measured a few minutes after they check in with a client new enough to run one.</p>';
            legendEl.innerHTML = '';
            return;
        }
        renderLegend(new Set(measured.map((h) => archKey(h.architecture))));

        const width = Math.max(280, Math.floor(chartEl.getBoundingClientRect().width));
        const height = width < 600 ? 260 : 320;
        const m = { top: 14, right: 24, bottom: 40, left: 52 };
        const w = width - m.left - m.right;
        const hgt = height - m.top - m.bottom;
        const xMax = niceMax(Math.max(...measured.map((h) => h.benchmark.single_thread_mbps)));
        const yMax = niceMax(Math.max(...measured.map((h) => h.benchmark.multi_thread_mbps)));
        const x = (v) => m.left + (v / xMax) * w;
        const y = (v) => m.top + hgt - (v / yMax) * hgt;

        const ticks = (max) => [0, 0.25, 0.5, 0.75, 1].map((f) => f * max);
        const fmtTick = (v) => (v >= 10 || v === 0 ? Math.round(v) : v.toFixed(1));

        let svg = `<svg class="hw-svg" width="100%" viewBox="0 0 ${width} ${height}" preserveAspectRatio="xMinYMin meet" role="img"
            aria-label="Scatter of one-core against all-core throughput for ${measured.length} hosts. The table below has the same numbers.">`;
        for (const t of ticks(yMax)) {
            svg += `<line class="hw-grid" x1="${m.left}" x2="${m.left + w}" y1="${y(t)}" y2="${y(t)}"/>`;
            svg += `<text class="hw-tick" x="${m.left - 8}" y="${y(t)}" text-anchor="end" dominant-baseline="middle">${fmtTick(t)}</text>`;
        }
        for (const t of ticks(xMax)) {
            svg += `<text class="hw-tick" x="${x(t)}" y="${m.top + hgt + 16}" text-anchor="middle">${fmtTick(t)}</text>`;
        }
        svg += `<line class="hw-axis" x1="${m.left}" x2="${m.left + w}" y1="${y(0)}" y2="${y(0)}"/>`;
        svg += `<text class="hw-axis-title" x="${m.left + w / 2}" y="${height - 4}" text-anchor="middle">One core, MB/s</text>`;
        svg += `<text class="hw-axis-title" transform="translate(12 ${m.top + hgt / 2}) rotate(-90)" text-anchor="middle">All cores, MB/s</text>`;

        // Points first, biggest score last so the leaders sit on top where
        // they overlap. Labels go to the right of a point, or the left, and are
        // dropped where neither fits; the tooltip still names every point.
        const order = [...measured].sort((a, b) => a.benchmark.multi_thread_mbps - b.benchmark.multi_thread_mbps);
        const placed = [];
        const overlaps = (r) => placed.some((p) => r.x1 < p.x2 && r.x2 > p.x1 && r.y1 < p.y2 && r.y2 > p.y1);
        const pts = order.map((h) => ({ h, cx: x(h.benchmark.single_thread_mbps), cy: y(h.benchmark.multi_thread_mbps) }));
        // Points are obstacles to labels too.
        for (const p of pts) placed.push({ x1: p.cx - 7, x2: p.cx + 7, y1: p.cy - 7, y2: p.cy + 7 });

        let labels = '';
        for (const p of [...pts].reverse()) {
            const tw = p.h.hostname.length * 7 + 4;
            const tries = [
                { x1: p.cx + 10, y1: p.cy - 8, anchor: 'start', tx: p.cx + 10 },
                { x1: p.cx - 10 - tw, y1: p.cy - 8, anchor: 'end', tx: p.cx - 10 },
                { x1: p.cx - tw / 2, y1: p.cy - 24, anchor: 'middle', tx: p.cx },
                { x1: p.cx - tw / 2, y1: p.cy + 8, anchor: 'middle', tx: p.cx },
            ];
            for (const t of tries) {
                const r = { x1: t.x1, x2: t.x1 + tw, y1: t.y1, y2: t.y1 + 16 };
                if (r.x1 < m.left || r.x2 > width - 2 || r.y1 < 0 || r.y2 > m.top + hgt) continue;
                if (overlaps(r)) continue;
                placed.push(r);
                labels += `<text class="hw-label" x="${t.tx}" y="${t.y1 + 8}" text-anchor="${t.anchor}" dominant-baseline="middle">${escapeHtml(p.h.hostname)}</text>`;
                break;
            }
        }

        for (const p of pts) {
            svg += `<circle class="hw-point hw-arch-${archKey(p.h.architecture)}" cx="${p.cx}" cy="${p.cy}" r="6"/>`;
        }
        svg += labels;
        // Hit targets are bigger than the marks, and drawn last so they win.
        for (const p of pts) {
            svg += `<circle class="hw-hit" data-host="${escapeHtml(p.h.hostname)}" cx="${p.cx}" cy="${p.cy}" r="14"/>`;
        }
        svg += '</svg>';
        chartEl.innerHTML = svg;
    }

    function showTooltip(host, evt) {
        const h = hosts.find((o) => o.hostname === host);
        if (!h || !bench(h)) return;
        const b = h.benchmark;
        tooltip.innerHTML = `<div class="hw-tt-host">${escapeHtml(h.hostname)}</div>
            <div class="hw-tt-cpu">${escapeHtml(cleanModel(h.cpu_model))}${h.virtualization ? ` · VM` : ''}</div>
            <dl>
                <dt>All cores</dt><dd>${formatMBps(b.multi_thread_mbps)} MB/s <span>(${b.threads} threads)</span></dd>
                <dt>One core</dt><dd>${formatMBps(b.single_thread_mbps)} MB/s</dd>
                ${h.memory_total_bytes ? `<dt>Memory</dt><dd>${formatBytes(h.memory_total_bytes)}</dd>` : ''}
            </dl>`;
        tooltip.hidden = false;
        const pad = 14;
        const tw = tooltip.offsetWidth, th = tooltip.offsetHeight;
        let left = evt.clientX + pad, top = evt.clientY + pad;
        if (left + tw > window.innerWidth - 8) left = evt.clientX - tw - pad;
        if (top + th > window.innerHeight - 8) top = evt.clientY - th - pad;
        tooltip.style.left = `${Math.max(8, left)}px`;
        tooltip.style.top = `${Math.max(8, top)}px`;
    }

    chartEl.addEventListener('pointermove', (e) => {
        const hit = e.target.closest('.hw-hit');
        if (hit) showTooltip(hit.dataset.host, e);
        else tooltip.hidden = true;
    });
    chartEl.addEventListener('pointerleave', () => { tooltip.hidden = true; });

    // ---- Data and actions ----------------------------------------------

    function render() {
        renderTable();
        renderChart();
    }

    function settlePending() {
        for (const [host, p] of pending) {
            const h = hosts.find((o) => o.hostname === host);
            const done = h && h.benchmark && h.benchmark.id === p.id;
            if (done || Date.now() - p.at > FAST_POLL_FOR_MS) {
                pending.delete(host);
                if (!done) statusEl.textContent = `${host} accepted the benchmark but no result has arrived yet; it will show up here when it does.`;
            }
        }
        if (!pending.size && fastPoll) {
            clearInterval(fastPoll);
            fastPoll = null;
        }
    }

    function load() {
        return fetch('/api/hardware').then((res) => {
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            return res.json();
        }).then((data) => {
            hosts = data || [];
            settlePending();
            if (statusEl.dataset.loading) {
                statusEl.textContent = '';
                delete statusEl.dataset.loading;
            }
            render();
        }).catch((err) => {
            statusEl.textContent = `Could not load hardware: ${err.message}`;
        });
    }

    function runBenchmark(host, btn) {
        btn.disabled = true;
        statusEl.textContent = '';
        fetch(`/api/systems/${encodeURIComponent(host)}/benchmark`, { method: 'POST' })
            .then((res) => res.json().catch(() => ({})).then((body) => ({ res, body })))
            .then(({ res, body }) => {
                if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
                pending.set(host, { id: body.id, at: Date.now() });
                if (!fastPoll) fastPoll = setInterval(load, FAST_POLL_MS);
                render();
            })
            .catch((err) => {
                statusEl.textContent = `${host}: ${err.message}`;
                btn.disabled = false;
            });
    }

    rowsEl.addEventListener('click', (e) => {
        const btn = e.target.closest('.hw-run');
        if (btn) runBenchmark(btn.dataset.host, btn);
    });

    function setRank(r) {
        rank = RANKERS[r] ? r : 'multi';
        rankButtons.forEach((b) => b.setAttribute('aria-checked', String(b.dataset.rank === rank)));
        try {
            localStorage.setItem('muc-hw-rank', rank);
        } catch (e) { /* not remembered; nothing else depends on it */ }
        renderTable();
    }
    rankButtons.forEach((b) => b.addEventListener('click', () => setRank(b.dataset.rank)));

    let resizeTimer = null;
    window.addEventListener('resize', () => {
        clearTimeout(resizeTimer);
        resizeTimer = setTimeout(renderChart, 120);
    });

    setRank(rank);
    statusEl.textContent = 'Loading…';
    statusEl.dataset.loading = '1';
    load();
    setInterval(() => { if (!document.hidden) load(); }, REFRESH_MS);
})();
