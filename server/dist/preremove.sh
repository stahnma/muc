#!/bin/sh
systemctl stop muc-server 2>/dev/null || true
systemctl disable muc-server 2>/dev/null || true
