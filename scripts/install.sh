#!/bin/sh
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 022

fail() { printf '%s\n' "$*" >&2; exit 1; }
usage() { printf '%s\n' 'Usage: sh install.sh --allow-metrics [--check]' 'Installs local metrics collection only; account enrollment is not available.'; }
allow=false
check=false
for arg in "$@"; do
    case "$arg" in
        --allow-metrics) allow=true ;;
        --check) check=true ;;
        --help) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done
[ "$allow" = true ] || fail 'Explicit --allow-metrics consent is required.'
[ "$(uname -s)" = Linux ] || fail 'Linux is required.'
[ "$(id -u)" -eq 0 ] || fail 'Run the installer as root (sudo). The agent runs unprivileged.'
[ -d /run/systemd/system ] || fail 'A running systemd system instance is required.'
for command in systemctl install mktemp; do
    command -v "$command" >/dev/null || fail "Missing command: $command"
done
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$root/bin/kladit-agent" ] || fail 'Missing bin/kladit-agent; build the Linux binary first.'
for name in kladit-agent.service kladit-agent.timer; do
    [ -f "$root/packaging/systemd/$name" ] || fail "Missing unit: $name"
    [ ! -e "/etc/systemd/system/$name" ] && [ ! -L "/etc/systemd/system/$name" ] || fail "Already installed: $name"
done
for path in /etc/kladit-agent /usr/local/lib/kladit-agent; do
    [ ! -e "$path" ] && [ ! -L "$path" ] || fail "Already exists: $path; automatic replacement is not supported."
done
if [ "$check" = true ]; then
    printf '%s\n' 'Installation prerequisites passed. No files changed.'
    exit 0
fi
# Copy the reviewed bundle into administrator-owned paths before starting the service.
install -d -o root -g root -m 0755 /etc/kladit-agent /usr/local/lib/kladit-agent
install -o root -g root -m 0755 "$root/bin/kladit-agent" /usr/local/lib/kladit-agent/kladit-agent
printf '%s\n' '{"version":1,"capabilities":{"metrics.read":true}}' > /etc/kladit-agent/policy.json
chmod 0644 /etc/kladit-agent/policy.json
for name in kladit-agent.service kladit-agent.timer; do
    install -o root -g root -m 0644 "$root/packaging/systemd/$name" "/etc/systemd/system/$name"
done
systemctl daemon-reload
# A failed first collection must not leave a periodically restarting installation.
if ! systemctl start kladit-agent.service; then
    fail 'Initial collection failed. Inspect journalctl -u kladit-agent.service. Timer was not enabled.'
fi
systemctl enable --now kladit-agent.timer
printf '%s\n' 'Local metrics collection installed. Account enrollment is not configured.'
