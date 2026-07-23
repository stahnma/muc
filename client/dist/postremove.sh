#!/bin/sh
# On upgrade ($1 == 1 for RPM, "upgrade" for deb), restart the service.
# This runs after the old package's preremove, which may have stopped it.
if [ "$1" = "upgrade" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  systemctl daemon-reload
  systemctl try-restart muc-client 2>/dev/null || true
else
  # Full removal: drop the "run as root" override installed by postinstall.
  rm -f /etc/systemd/system/muc-client.service.d/10-root.conf 2>/dev/null || true
  # Also clean up the previous zypper-only drop-in name from older packages.
  rm -f /etc/systemd/system/muc-client.service.d/10-zypper-root.conf 2>/dev/null || true
  rmdir /etc/systemd/system/muc-client.service.d 2>/dev/null || true
  systemctl daemon-reload 2>/dev/null || true
fi
