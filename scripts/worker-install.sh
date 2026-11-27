#!/bin/sh
# Explicit Docker-socket consent for an already installed connected agent.
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 077
fail() { printf '%s\n' "$*" >&2; exit 1; }
[ "$#" -ge 1 ] && [ "$#" -le 2 ] && [ "$1" = --allow-docker-worker ] || fail 'Usage: sh scripts/worker-install.sh --allow-docker-worker [--check]'
[ "${2:-}" = '' ] || [ "$2" = --check ] || fail 'Unknown worker installation option.'
[ "$(id -u)" -eq 0 ] && [ -d /run/systemd/system ] || fail 'Running Linux systemd and root are required.'
for command in systemctl install getent useradd stat; do
    command -v "$command" >/dev/null || fail "Missing command: $command"
done
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
for path in /etc/kladit-agent /usr/local/lib/kladit-agent; do
    [ -d "$path" ] && [ ! -L "$path" ] && [ "$(stat -c %u "$path")" -eq 0 ] || fail 'Expected a managed agent installation.'
    mode=$(stat -c %a "$path")
    [ "$mode" = 755 ] || fail 'Agent directories must not be writable by another user.'
done
for path in /etc/kladit-agent/token /etc/kladit-agent/connection.json /usr/local/lib/kladit-agent/kladit-agent; do
    [ -f "$path" ] && [ ! -L "$path" ] && [ "$(stat -c %u "$path")" -eq 0 ] || fail 'Expected existing connected agent credentials/binary.'
done
token_mode=$(stat -c %a /etc/kladit-agent/token)
[ "$token_mode" = 600 ] || [ "$token_mode" = 400 ] || fail 'Machine token must be private.'
[ "$(stat -c %a /etc/kladit-agent/connection.json)" = 644 ] || fail 'Worker needs a root-owned readable connection config.'
[ "$(stat -c %a /usr/local/lib/kladit-agent/kladit-agent)" = 755 ] || fail 'Worker needs an executable root-owned agent binary.'
[ -f "$root/packaging/systemd/kladit-worker.service" ] || fail 'Missing worker unit.'
for path in /etc/kladit-agent/worker-policy.json /etc/systemd/system/kladit-worker.service /var/lib/kladit-worker; do
    [ ! -e "$path" ] && [ ! -L "$path" ] || fail 'Worker profile/state already exists; automatic replacement is not supported.'
done
[ -S /run/docker.sock ] || fail 'Rootful Docker Engine socket is required.'
getent group docker >/dev/null || fail 'Docker group is required.'
# An unrelated existing account must not inherit this permission.
! getent passwd kladit-worker >/dev/null || fail 'User kladit-worker already exists; review it before enabling a worker.'
/usr/local/lib/kladit-agent/kladit-agent worker --help >/dev/null 2>&1 || fail 'Update the agent binary to a worker-capable version first.'
if [ "${2:-}" = --check ]; then
    printf '%s\n' 'Worker installation prerequisites passed. No changes made.'
    exit 0
fi
printf '%s\n' 'Docker socket access grants host-level authority. Enable only for your own trusted tasks on a dedicated machine.' >&2
useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin kladit-worker
# The explicit unit grants Docker group membership only to this worker service.
cat > /etc/kladit-agent/worker-policy.json <<'POLICY'
{"version":1,"capabilities":{"jobs.execute":true},"limits":{"cpu_millis":1000,"memory_bytes":1073741824,"timeout_seconds":3600},"min_free_disk_bytes":1073741824,"min_free_memory_bytes":67108864}
POLICY
chmod 0644 /etc/kladit-agent/worker-policy.json
install -o root -g root -m 0644 "$root/packaging/systemd/kladit-worker.service" /etc/systemd/system/kladit-worker.service
systemctl daemon-reload
systemctl enable --now kladit-worker.service
printf '%s\n' 'Worker foundation enabled. Container execution remains disabled in this build.'
