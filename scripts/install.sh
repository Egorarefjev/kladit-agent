#!/bin/sh
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 022

fail() { printf '%s\n' "$*" >&2; exit 1; }
usage() {
    printf '%s\n' 'Usage: sh install.sh --allow-metrics [--config PATH --token-file PATH] [--check]'
}
allow=false
check=false
config=
token=
enroll_url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --allow-metrics) allow=true ;;
        --check) check=true ;;
        --config|--token-file|--enroll-url)
            [ "$#" -ge 2 ] && [ -n "$2" ] || fail "Missing value for $1"
            case "$1" in
                --enroll-url) enroll_url=$2 ;;
                --config) config=$2 ;;
                --token-file) token=$2 ;;
            esac
            shift ;;
        --help) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
    shift
done
[ -z "$enroll_url" ] || { [ -z "$config" ] && [ -z "$token" ]; } || fail "Enrollment cannot be combined with machine credentials."
[ "$allow" = true ] || fail 'Explicit --allow-metrics consent is required.'
if [ -n "$config" ] || [ -n "$token" ]; then
    [ -n "$config" ] && [ -n "$token" ] || fail '--config and --token-file must be supplied together.'
    [ -f "$config" ] && [ -f "$token" ] || fail 'Connection config or token file is missing.'
fi
[ "$(uname -s)" = Linux ] || fail 'Linux is required.'
[ "$(id -u)" -eq 0 ] || fail 'Run the installer as root (sudo). The agent runs unprivileged.'
[ -d /run/systemd/system ] || fail 'A running systemd system instance is required.'
for command in systemctl install mktemp; do
    command -v "$command" >/dev/null || fail "Missing command: $command"
done
systemd_version=$(systemctl --version | head -n 1 | cut -d ' ' -f 2)
[ "$systemd_version" -ge 247 ] || fail 'systemd 247+ is required.'
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -f "$root/bin/kladit-agent" ] || fail 'Missing bin/kladit-agent; build the Linux binary first.'
for name in kladit-agent.service kladit-agent.timer; do
    [ -f "$root/packaging/systemd/$name" ] || fail "Missing unit: $name"
    [ ! -e "/etc/systemd/system/$name" ] && [ ! -L "/etc/systemd/system/$name" ] || fail "Already installed: $name"
done
for path in /etc/kladit-agent /usr/local/lib/kladit-agent /etc/systemd/system/kladit-agent.service.d; do
    [ ! -e "$path" ] && [ ! -L "$path" ] || fail "Already exists: $path; automatic replacement is not supported."
done
if [ -n "$config$enroll_url" ]; then
    [ -f "$root/packaging/systemd/connection.conf" ] || fail 'Missing connection.conf unit override.'
fi
if [ "$check" = true ]; then
    printf '%s\n' 'Installation prerequisites passed. No files changed.'
    exit 0
fi
if [ -n "$enroll_url" ]; then
    stage=$(mktemp -d)
    trap 'rm -rf "$stage"' EXIT HUP INT TERM
    "$root/bin/kladit-agent" enroll --url "$enroll_url" --directory "$stage"
    config=$stage/connection.json
    token=$stage/token
fi
install -d -o root -g root -m 0755 /etc/kladit-agent /usr/local/lib/kladit-agent
install -o root -g root -m 0755 "$root/bin/kladit-agent" /usr/local/lib/kladit-agent/kladit-agent
printf '%s\n' '{"version":1,"capabilities":{"metrics.read":true}}' > /etc/kladit-agent/policy.json
chmod 0644 /etc/kladit-agent/policy.json
for name in kladit-agent.service kladit-agent.timer; do
    install -o root -g root -m 0644 "$root/packaging/systemd/$name" "/etc/systemd/system/$name"
done
if [ -n "$config" ]; then
    install -o root -g root -m 0644 "$config" /etc/kladit-agent/connection.json
    install -o root -g root -m 0600 "$token" /etc/kladit-agent/token
    install -d -o root -g root -m 0755 /etc/systemd/system/kladit-agent.service.d
    install -o root -g root -m 0644 "$root/packaging/systemd/connection.conf" /etc/systemd/system/kladit-agent.service.d/connection.conf
fi
systemctl daemon-reload
if ! systemctl start kladit-agent.service; then
    fail 'Initial collection or delivery failed. Inspect journalctl -u kladit-agent.service. Timer was not enabled.'
fi
systemctl enable --now kladit-agent.timer
printf '%s\n' 'Kladit Agent installed.'

