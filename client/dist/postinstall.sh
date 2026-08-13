#!/bin/sh
# muc-client runs as root (see muc-client.service). It used to default to the
# unprivileged muc user with a 10-root.conf drop-in added on hosts whose package
# manager needed more, which by the end meant every host this is packaged for:
#
#   - apt (Debian, Ubuntu) refreshes via `apt-get update`, which writes to
#     root-owned /var/lib/apt/lists. apt-daily.timer keeps that cache warm only
#     intermittently and is easily disabled, so the client refreshes it itself.
#   - zypper (openSUSE, SUSE) refreshes only as root, and the distro ships no
#     periodic refresh timer at all.
#   - dnf/yum CAN refresh unprivileged — but into a per-user cache that the
#     administrator's own `sudo dnf` never reads. The client pins
#     metadata_expire=1h (see updates_dnf.go) while stock RHEL/Rocky/Fedora repos
#     ship 6h for interactive use, so between those two marks the client had
#     re-synced and the shell had not: the dashboard correctly listed updates
#     that `sudo dnf update` called "nothing to do". Measured on a Fedora 44
#     host — client cache 27 minutes old listing 14 updates, root's 5h48m old
#     listing none, `dnf check-update --refresh` then returning exactly those 14.
#     Sharing /var/cache/dnf means the client's refresh warms the very cache the
#     administrator reads, so there is only one answer to give.
#
# A repository with repo_gpgcheck=1 verifies repomd.xml against a GPG keyring
# under the cache dir, separate from the system rpm keyring. The client passes -y
# so it adopts those keys unattended; without that the import prompt is declined
# and skip_if_unavailable drops the repo along with all of its updates. Each
# import is logged, so `journalctl -u muc-client | grep "imported repository
# signing keys"` shows what the host has trusted. On the shared cache that
# keyring is the one the administrator's dnf consults too, not a muc-owned copy.

# Retire the drop-in: the shipped unit now sets User=root directly, and leaving a
# stale override behind would keep applying settings this package no longer
# manages — including the StateDirectory that caused the ownership bug below.
DROPIN_DIR=/etc/systemd/system/muc-client.service.d
rm -f "$DROPIN_DIR/10-root.conf" 2>/dev/null || true
rm -f "$DROPIN_DIR/10-zypper-root.conf" 2>/dev/null || true
rmdir "$DROPIN_DIR" 2>/dev/null || true

# Clean up state the client no longer keeps.
#
# It never writes anything now — the only write it ever had was an unprivileged
# dnf metadata cache, and root uses /var/cache/dnf. Two things are left over on
# upgraded hosts:
#
#   - /var/lib/muc/dnf, that cache. Nothing reads it, and a full set of repo
#     metadata is not small (64 MB on the host this was developed against).
#   - ownership of /var/lib/muc itself. Earlier versions set StateDirectory=muc,
#     and systemd re-chowns a StateDirectory to the unit's user on every start,
#     recursively — so every host that ran this client as root ended up with a
#     root-owned /var/lib/muc. Where muc-server is installed alongside, that is
#     its database directory and it runs as muc, so it loses write access the
#     next time it restarts. The failure is invisible until then: the server
#     keeps a writable fd opened before the chown, and permissions are checked at
#     open(), not per write.
#
# muc-server owns /var/lib/muc now; the client package neither creates nor uses
# it. Repair rather than remove, since the server's systems.db may be in there.
if [ -d /var/lib/muc ]; then
    rm -rf /var/lib/muc/dnf 2>/dev/null || true
    if getent passwd muc >/dev/null 2>&1; then
        chown -R muc:muc /var/lib/muc 2>/dev/null || true
    fi

    # On a client-only host the directory is now an empty leftover from older
    # client packages, so retire it. Two independent guards, because getting this
    # wrong deletes the server's database:
    #
    #   - skip entirely if muc-server is installed, whatever the directory holds;
    #   - rmdir, never rm -r. rmdir refuses a non-empty directory, so even if the
    #     first guard is somehow wrong, systems.db cannot be destroyed.
    #
    # The muc user is deliberately NOT removed. This package does not own it —
    # muc-server's preinstall creates it too, and a client upgrade has no reliable
    # way to know the server is not about to need it. Removing system users on
    # upgrade also invites UID reuse, which silently hands any file still owned by
    # that UID to whatever service claims it next.
    if [ ! -f /usr/lib/systemd/system/muc-server.service ]; then
        rmdir /var/lib/muc 2>/dev/null || true
    fi
fi

systemctl daemon-reload
systemctl try-restart muc-client

# Watch the package database so a `dnf upgrade` is reflected on the dashboard in
# seconds rather than at the next poll. Not fatal if it cannot be enabled — the
# client still polls.
systemctl enable --now muc-client-recheck.path 2>/dev/null || true
