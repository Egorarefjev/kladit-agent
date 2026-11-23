#!/bin/sh
# Explicit binary-only update; policy, credentials, units and overrides stay owned by the installation.
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 077
fail() { printf '%s\n' "$*" >&2; exit 1; }
[ "$#" -eq 0 ] || fail 'Usage: sh scripts/update.sh'
[ "$(id -u)" -eq 0 ] || fail 'Run the updater as root (sudo).'
[ "$(uname -s)" = Linux ] && [ -d /run/systemd/system ] || fail 'Running Linux systemd is required.'
for command in systemctl install mktemp flock stat; do
    command -v "$command" >/dev/null || fail "Missing command: $command"
done
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
directory=/usr/local/lib/kladit-agent
binary=$directory/kladit-agent
for path in "$directory" /etc/kladit-agent; do
    [ -d "$path" ] && [ ! -L "$path" ] && [ "$(stat -c %u "$path")" -eq 0 ] || fail "Not a managed installation: $path"
done
for path in "$binary" /etc/kladit-agent/policy.json /etc/systemd/system/kladit-agent.service /etc/systemd/system/kladit-agent.timer; do
    [ -f "$path" ] && [ ! -L "$path" ] && [ "$(stat -c %u "$path")" -eq 0 ] || fail "Not a managed installation: $path"
done
[ -x "$root/bin/kladit-agent" ] || fail 'Missing replacement binary.'
exec 9<"$directory"
flock -n 9 || { echo 'Agent update already running.' >&2; exit 70; }
current=$("$binary" version | cut -d ' ' -f 2)
target=$("$root/bin/kladit-agent" version | cut -d ' ' -f 2)
# Only published stable versions may be compared; never guess or silently downgrade.
awk -v current="$current" -v target="$target" 'BEGIN {
    pattern="^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$"
    if (current !~ pattern || target !~ pattern) exit 1
    split(current,c,"."); split(target,t,".")
    for(i=1;i<=3;i++) { if(t[i]+0>c[i]+0) exit 0; if(t[i]+0<c[i]+0) exit 1 }
}' || fail 'Unsupported version or attempted downgrade.'
if [ "$current" = "$target" ]; then
    echo 'Kladit Agent is already up to date.'
    exit 0
fi
timer_active=false
systemctl is-active --quiet kladit-agent.timer && timer_active=true
stage=$(mktemp -d "$directory/.update.XXXXXX")
stopped=false
swapped=false
committed=false
cleanup() {
    result=$?
    trap - EXIT HUP INT TERM
    if [ "$committed" = false ] && [ "$swapped" = true ]; then
        mv -f "$stage/previous" "$binary"
        systemctl start kladit-agent.service >/dev/null 2>&1 || true
    fi
    if [ "$stopped" = true ] && [ "$timer_active" = true ]; then
        systemctl start kladit-agent.timer || result=1
    fi
    rm -rf "$stage"
    exit "$result"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
cp -p "$binary" "$stage/previous"
install -o root -g root -m 0755 "$root/bin/kladit-agent" "$stage/replacement"
stopped=true
systemctl stop kladit-agent.timer kladit-agent.service
mv -f "$stage/replacement" "$binary"
swapped=true
if ! systemctl start kladit-agent.service; then
    # Exit 3 is reserved for delivery failures after local preparation succeeded.
    # Check normal process exit too: SIGQUIT also has status 3, but is a crash.
    state=$(systemctl show kladit-agent.service -p Result -p ExecMainCode -p ExecMainStatus)
    if printf '%s\n' "$state" | grep -qx 'Result=exit-code' &&
       printf '%s\n' "$state" | grep -qx 'ExecMainCode=1' &&
       printf '%s\n' "$state" | grep -qx 'ExecMainStatus=3'; then
        printf '%s\n' 'Agent binary is updated, but metrics delivery failed. Check the endpoint and service journal; delivery will retry on the existing timer.' >&2
    else
        fail 'Agent update failed locally; restoring the previous binary.'
    fi
fi
committed=true
printf '%s\n' "Kladit Agent updated from $current to $target."
