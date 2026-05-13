#!/bin/sh
# Only stop/disable on full removal, not on upgrade.
# RPM passes a count: 0 = removal, 1 = upgrade
# deb passes a string: "remove" = removal, "upgrade" = upgrade
if [ "$1" = "remove" ] || [ "$1" -eq 0 ] 2>/dev/null; then
  systemctl stop muc-client 2>/dev/null || true
  systemctl disable muc-client 2>/dev/null || true
fi
