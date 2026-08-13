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

systemctl daemon-reload
systemctl try-restart muc-client

# Watch the package database so a `dnf upgrade` is reflected on the dashboard in
# seconds rather than at the next poll. Not fatal if it cannot be enabled — the
# client still polls.
systemctl enable --now muc-client-recheck.path 2>/dev/null || true
