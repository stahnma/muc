#!/bin/sh
# On upgrade ($1 == 1 for RPM, "upgrade" for deb), restart the service.
# This runs after the old package's preremove, which may have stopped it.
if [ "$1" = "upgrade" ] || [ "$1" -ge 1 ] 2>/dev/null; then
  systemctl daemon-reload
  systemctl try-restart muc-server 2>/dev/null || true
fi
