#!/bin/sh
# Run only on the disposable systemd CI host after the standard fixture install.
set -eu
[ "$(id -u)" -eq 0 ] && [ "$#" -eq 2 ] || exit 1
bundle=$1
fixture=$2
binary=/usr/local/lib/kladit-agent/kladit-agent
service=/etc/systemd/system/kladit-agent.service
timer=/etc/systemd/system/kladit-agent.timer
override=/etc/systemd/system/kladit-agent.service.d/override.conf
stage=$(mktemp -d)
cleanup() {
    result=$?
    trap - EXIT
    if [ "$result" -ne 0 ]; then
        journalctl -u kladit-agent.service -u kladit-agent.timer -n 30 --no-pager
    fi
    rm -rf "$stage"
    exit "$result"
}
trap cleanup EXIT
cp /etc/kladit-agent/policy.json "$stage/policy"
cp /etc/kladit-agent/token "$stage/token"
cp "$override" "$stage/override"
target=$("$bundle/bin/kladit-agent" version)

prepare() {
    systemctl stop kladit-agent.timer kladit-agent.service
    install -m 0755 "$old/bin/kladit-agent" "$binary"
    cp "$stage/policy" /etc/kladit-agent/policy.json
    cp "$stage/token" /etc/kladit-agent/token
    cp "$fixture/connection.json" /etc/kladit-agent/connection.json
    cp "$stage/override" "$override"
    echo 204 > "$fixture/response-status"
    systemctl daemon-reload
    systemctl reset-failed kladit-agent.service kladit-agent.timer
}

check_state() {
    sha256sum --check "$stage/before"
    if [ "$active" = true ]; then
        systemctl is-active --quiet kladit-agent.timer
    else
        ! systemctl is-active --quiet kladit-agent.timer
    fi
}

for version in 0.2.1 0.3.0; do
    old=$stage/$version
    mkdir "$old"
    base=https://github.com/Egorarefjev/kladit-agent/releases/download/v$version
    name=kladit-agent-linux-amd64.tar.gz
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' "$base/$name" -o "$old/$name"
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' "$base/$name.sha256" -o "$old/$name.sha256"
    (cd "$old" && sha256sum --check "$name.sha256")
    tar -xzf "$old/$name" -C "$old"
    systemctl stop kladit-agent.timer kladit-agent.service
    # Retain the actual predecessor's units, including 0.2.1 PrivateDevices.
    cp "$old/packaging/systemd/kladit-agent.service" "$service"
    cp "$old/packaging/systemd/kladit-agent.timer" "$timer"
    cp "$old/packaging/systemd/connection.conf" /etc/systemd/system/kladit-agent.service.d/connection.conf
    for scenario in healthy dns refused tls http401 http500 bad-config bad-token denied-policy; do
        prepare
        active=true
        expected=0
        case "$scenario" in
            dns) printf '%s\n' '{"endpoint":"https://kladit-update-test.invalid/api/v1/agent/metrics"}' > /etc/kladit-agent/connection.json ;;
            refused) printf '%s\n' '{"endpoint":"https://127.0.0.1:1/api/v1/agent/metrics"}' > /etc/kladit-agent/connection.json; active=false ;;
            tls) printf '[Service]\nEnvironment=SSL_CERT_FILE=/dev/null\n' >> "$override" ;;
            http401) echo 401 > "$fixture/response-status" ;;
            http500) echo 500 > "$fixture/response-status" ;;
            bad-config) echo '{}' > /etc/kladit-agent/connection.json; expected=1 ;;
            bad-token) echo invalid-token > /etc/kladit-agent/token; expected=1 ;;
            denied-policy) echo '{"version":1,"capabilities":{"metrics.read":false}}' > /etc/kladit-agent/policy.json; expected=1 ;;
        esac
        systemctl daemon-reload
        if [ "$active" = true ]; then systemctl start kladit-agent.timer; fi
        sha256sum /etc/kladit-agent/* "$service" "$timer" /etc/systemd/system/kladit-agent.service.d/* > "$stage/before"
        result=0
        sh "$bundle/scripts/update.sh" || result=$?
        test "$result" -eq "$expected"
        check_state
        if [ "$expected" -eq 0 ]; then
            test "$("$binary" version)" = "$target"
            # Idempotent even while the receiver is unavailable.
            sh "$bundle/scripts/update.sh"
        else
            test "$("$binary" version)" = "kladit-agent $version"
        fi
        # Restore receiver/config and verify real delivery without re-enrollment.
        systemctl stop kladit-agent.timer kladit-agent.service
        cp "$stage/policy" /etc/kladit-agent/policy.json
        install -m 0600 "$stage/token" /etc/kladit-agent/token
        cp "$fixture/connection.json" /etc/kladit-agent/connection.json
        cp "$stage/override" "$override"
        echo 204 > "$fixture/response-status"
        systemctl daemon-reload
        rm -f "$fixture/received"
        # Scenarios deliberately restart units faster than the normal 30s cadence.
        systemctl reset-failed kladit-agent.service kladit-agent.timer
        systemctl start kladit-agent.service
        test -f "$fixture/received"
        printf '%s\n' "$version: $scenario passed"
    done
    # A broken executable and SIGQUIT (also status 3) must still roll back.
    mkdir -p "$stage/broken/bin" "$stage/broken/scripts"
    cp "$bundle/scripts/update.sh" "$stage/broken/scripts/"
    for failure in 'exit 1' 'kill -QUIT $$'; do
        prepare
        active=true
        systemctl start kladit-agent.timer
        printf '#!/bin/sh\nif [ "$1" = version ]; then echo "kladit-agent 0.4.1"; else %s; fi\n' "$failure" > "$stage/broken/bin/kladit-agent"
        chmod 0755 "$stage/broken/bin/kladit-agent"
        sha256sum /etc/kladit-agent/* "$service" "$timer" /etc/systemd/system/kladit-agent.service.d/* > "$stage/before"
        if sh "$stage/broken/scripts/update.sh"; then exit 1; fi
        test "$("$binary" version)" = "kladit-agent $version"
        check_state
    done
    chmod 0600 /etc/kladit-agent/token
    printf '%s\n' "$version: startup failure and signal rollback passed"
done
