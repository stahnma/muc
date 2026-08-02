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
# dnf/yum keep the unprivileged muc user: unlike apt and zypper, dnf can refresh
# metadata into a cache it owns without root. Note that dnf-makecache.timer does
# NOT help here — it warms root's /var/cache/dnf, which is a different tree from
# the one an unprivileged dnf uses. The client compensates by pinning its cache
# to $STATE_DIRECTORY (see muc-client.service) and by passing the repoid-glob
# form of metadata_expire, which per-repo settings cannot override.
#
# A repository with repo_gpgcheck=1 verifies repomd.xml against a per-repo GPG
# keyring under the cache dir, separate from the system rpm keyring. The client
# passes -y so it adopts those keys unattended; without that the import prompt is
# declined and skip_if_unavailable drops the repo along with all of its updates.
# Each import is logged to the journal, so `journalctl -u muc-client | grep
# "imported repository signing keys"` shows what the host has trusted.
#
# Install a drop-in to run as root only where the package manager requires it.
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

# Watch the package database so a `dnf upgrade` is reflected on the dashboard in
# seconds rather than at the next poll. Not fatal if it cannot be enabled — the
# client still polls.
systemctl enable --now muc-client-recheck.path 2>/dev/null || true
