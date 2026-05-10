#!/bin/sh
systemctl daemon-reload
systemctl try-restart muc-server
