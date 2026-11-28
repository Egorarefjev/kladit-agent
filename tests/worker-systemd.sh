#!/bin/sh
# Disposable CI host only: an actual systemd worker, TLS sync and credential sandbox.
set -eu
bundle=$1
fixture=$2
ca=/run/kladit-agent-ci-ca.pem
before=$(sha256sum /etc/kladit-agent/policy.json /etc/kladit-agent/token)
# Simulate an earlier installed uninstaller retained by a binary-only update.
python3 - <<'LEGACY'
from pathlib import Path
p = Path('/usr/local/lib/kladit-agent/uninstall.sh')
s = p.read_text()
start = s.index('if [ -e /etc/systemd/system/kladit-worker.service ]')
end = s.index('systemctl disable --now kladit-agent.timer', start)
p.write_text(s[:start] + s[end:])
LEGACY
legacy_helper=$(sha256sum /usr/local/lib/kladit-agent/uninstall.sh)
sh "$bundle/scripts/worker-install.sh" --allow-docker-worker --check
test "$legacy_helper" = "$(sha256sum /usr/local/lib/kladit-agent/uninstall.sh)"
install -d /etc/systemd/system/kladit-worker.service.d
printf '[Service]\nEnvironment=SSL_CERT_FILE=%s\n' "$ca" > /etc/systemd/system/kladit-worker.service.d/ci.conf
sh "$bundle/scripts/worker-install.sh" --allow-docker-worker
cmp "$bundle/scripts/uninstall.sh" /usr/local/lib/kladit-agent/uninstall.sh
for cycle in 1 2; do
    rm -f "$fixture/worker-generation"
    systemctl restart kladit-worker.service
    attempt=0
    while [ ! -s "$fixture/worker-generation" ] && [ "$attempt" -lt 100 ]; do
        sleep 0.1
        attempt=$((attempt + 1))
    done
    if [ ! -s "$fixture/worker-generation" ]; then
        journalctl -u kladit-worker.service -n 30 --no-pager
        exit 1
    fi
    systemctl is-active --quiet kladit-worker.service
    if [ "$cycle" = 1 ]; then
        generation=$(cat "$fixture/worker-generation")
    else
        test "$generation" = "$(cat "$fixture/worker-generation")"
    fi
    test "$(stat -c %a /var/lib/kladit-worker)" = 700
    test "$(stat -c %a /var/lib/kladit-worker/state.json)" = 600
    test "$(stat -c %a /run/kladit-worker/token)" = 600
    test "$(systemctl show kladit-worker.service -p SupplementaryGroups --value)" = docker
    if systemctl show kladit-agent.service -p SupplementaryGroups --value | grep -qw docker; then exit 1; fi
done
# Existing binary lifecycle must refuse before stopping the metrics timer or losing state.
if worker_update_result=$(sh "$bundle/scripts/update.sh" 2>&1); then exit 1; fi
printf '%s\n' "$worker_update_result" | grep -q 'Worker profile installed'
if worker_remove_result=$(sh /usr/local/lib/kladit-agent/uninstall.sh --confirm --purge 2>&1); then exit 1; fi
printf '%s\n' "$worker_remove_result" | grep -q 'Worker profile installed'
systemctl is-active --quiet kladit-agent.timer
test "$before" = "$(sha256sum /etc/kladit-agent/policy.json /etc/kladit-agent/token)"
systemctl disable --now kladit-worker.service
# Ownership evidence for cleanup on this disposable CI host, never a user machine.
python3 - "$generation" <<'PY'
import json,sys
from pathlib import Path
state=json.loads(Path('/var/lib/kladit-worker/state.json').read_text())
assert state['generation']==sys.argv[1] and state['current'] is None
PY
rm -f /etc/systemd/system/kladit-worker.service /etc/systemd/system/kladit-worker.service.d/ci.conf /etc/kladit-agent/worker-policy.json
rmdir /etc/systemd/system/kladit-worker.service.d
rm -rf /var/lib/kladit-worker
userdel kladit-worker
systemctl daemon-reload
