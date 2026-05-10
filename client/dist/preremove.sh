#!/bin/sh
systemctl stop muc-client 2>/dev/null || true
systemctl disable muc-client 2>/dev/null || true
