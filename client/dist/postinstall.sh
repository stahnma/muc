#!/bin/sh
# Some package managers can only refresh their repository metadata as root, so
# the muc-client update check silently under-reports (typically as zero updates,
# surfaced on the dashboard as "unknown") when the daemon runs unprivileged:
#
#   - zypper (openSUSE, SUSE Linux Enterprise) refreshes only as root and the
#     distro ships no periodic refresh timer.
#   - apt (Debian, Ubuntu) refreshes via `apt-get update`, which writes to
#     root-owned /var/lib/apt/lists. apt-daily.timer keeps the cache warm only
#     intermittently and is easily disabled or skipped, so the client refreshes
#     the cache itself — and that needs root.
#
# dnf/yum are unaffected: run unprivileged they fall back to a per-user metadata
# cache, and dnf-makecache.timer keeps the system cache warm. On those hosts the
# daemon keeps the unprivileged muc user. Install a drop-in to run as root only
# where the package manager requires it.
DROPIN_DIR=/etc/systemd/system/muc-client.service.d
DROPIN="$DROPIN_DIR/10-root.conf"
# Clean up the previous zypper-only drop-in name from older packages.
rm -f "$DROPIN_DIR/10-zypper-root.conf" 2>/dev/null || true
if command -v zypper >/dev/null 2>&1 || command -v apt-get >/dev/null 2>&1; then
    mkdir -p "$DROPIN_DIR"
    cat > "$DROPIN" <<'EOF'
# Managed by muc-client packaging: this host's package manager can only refresh
# its repository metadata as root, so the update check under-reports when run
# unprivileged.
[Service]
User=root
Group=root
EOF
else
    rm -f "$DROPIN" 2>/dev/null || true
    rmdir "$DROPIN_DIR" 2>/dev/null || true
fi

systemctl daemon-reload
systemctl try-restart muc-client
