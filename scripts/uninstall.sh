#!/bin/sh
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
if [ "$#" -ne 1 ] || [ "$1" != --confirm ]; then
    printf '%s\n' 'Usage: sudo sh uninstall.sh --confirm' >&2
    exit 2
fi
[ "$(id -u)" -eq 0 ] || { printf '%s\n' 'Root is required.' >&2; exit 1; }
systemctl disable --now kladit-agent.timer
systemctl stop kladit-agent.service
rm -f /etc/systemd/system/kladit-agent.timer /etc/systemd/system/kladit-agent.service
if [ -d /etc/systemd/system/kladit-agent.service.d ]; then
    rm -f /etc/systemd/system/kladit-agent.service.d/connection.conf
    rmdir /etc/systemd/system/kladit-agent.service.d
fi
rm -f /usr/local/lib/kladit-agent/kladit-agent /etc/kladit-agent/token
rmdir /usr/local/lib/kladit-agent
systemctl daemon-reload
printf '%s\n' 'Agent and local token removed. Policy, connection config and journal logs preserved.'
