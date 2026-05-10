#!/bin/sh
if ! getent group muc >/dev/null 2>&1; then
    groupadd --system muc
fi
if ! getent passwd muc >/dev/null 2>&1; then
    useradd --system --gid muc --home-dir /var/lib/muc --shell /usr/sbin/nologin muc
fi
