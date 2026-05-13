#!/bin/sh
if ! getent group muc >/dev/null 2>&1; then
    groupadd --system muc
fi
if ! getent passwd muc >/dev/null 2>&1; then
    useradd --system --gid muc --home-dir /var/lib/muc --shell /usr/sbin/nologin muc
fi

if [ ! -d /var/lib/muc ]; then
    mkdir -p /var/lib/muc
fi
chown muc:muc /var/lib/muc
chmod 0750 /var/lib/muc
