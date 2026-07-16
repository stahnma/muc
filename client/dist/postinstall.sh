#!/bin/sh
# On zypper-based systems (openSUSE, SUSE Linux Enterprise) the client must run
# as root. Unlike dnf/apt — whose metadata caches are kept warm by the distro's
# own dnf-makecache.timer / apt-daily.timer — zypper only refreshes its metadata
# when invoked as root, and openSUSE ships no periodic refresh. Run unprivileged
# there and `zypper list-updates` reports against a stale cache, silently
# under-reporting (typically as zero updates). Install a drop-in so the daemon
# runs as root on those hosts; everywhere else it keeps the unprivileged muc user.
DROPIN_DIR=/etc/systemd/system/muc-client.service.d
DROPIN="$DROPIN_DIR/10-zypper-root.conf"
if command -v zypper >/dev/null 2>&1; then
    mkdir -p "$DROPIN_DIR"
    cat > "$DROPIN" <<'EOF'
# Managed by muc-client packaging: zypper can only refresh its repository
# metadata as root, so the update check under-reports when run unprivileged.
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
