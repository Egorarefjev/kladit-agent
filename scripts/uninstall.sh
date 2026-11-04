#!/bin/sh
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
if [ "$#" -lt 1 ] || [ "$#" -gt 2 ] || [ "$1" != --confirm ] || { [ "$#" -eq 2 ] && [ "$2" != --purge ]; }; then
    printf '%s\n' 'Usage: sudo sh /usr/local/lib/kladit-agent/uninstall.sh --confirm [--purge]' >&2
    exit 2
fi
[ "$(id -u)" -eq 0 ] || { printf '%s\n' 'Root is required.' >&2; exit 1; }
systemctl disable --now kladit-agent.timer
systemctl stop kladit-agent.service
rm -f /etc/systemd/system/kladit-agent.timer /etc/systemd/system/kladit-agent.service
if [ -d /etc/systemd/system/kladit-agent.service.d ]; then
    rm -f /etc/systemd/system/kladit-agent.service.d/connection.conf
    rmdir --ignore-fail-on-non-empty /etc/systemd/system/kladit-agent.service.d
fi
rm -f /usr/local/lib/kladit-agent/kladit-agent /usr/local/lib/kladit-agent/uninstall.sh /etc/kladit-agent/token
rmdir /usr/local/lib/kladit-agent
systemctl daemon-reload
if [ "${2:-}" = --purge ]; then
    rm -f /etc/kladit-agent/policy.json /etc/kladit-agent/connection.json
    rmdir /etc/kladit-agent
    printf '%s\n' 'Agent and configuration removed. Journal logs preserved.'
else
    printf '%s\n' 'Agent and local token removed. Policy, connection config and journal logs preserved.'
fi
