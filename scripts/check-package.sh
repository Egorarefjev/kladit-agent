#!/bin/sh
set -eu
[ "$#" -eq 2 ] || { echo 'Usage: sh scripts/check-package.sh BUNDLE_DIR VERSION' >&2; exit 1; }
cd "$1"
[ "$(find . -maxdepth 1 -type f | wc -l)" -eq 5 ]
sh -n install.sh
! grep -q '@BUNDLE_BASE@\|/latest/' install.sh
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
for arch in amd64 arm64; do
    name=kladit-agent-linux-$arch.tar.gz
    sha256sum --check "$name.sha256"
    mkdir "$stage/$arch"
    tar -xzf "$name" -C "$stage/$arch"
    test -x "$stage/$arch/bin/kladit-agent"
    go version -m "$stage/$arch/bin/kladit-agent" > "$stage/build-info"
    grep -q "GOARCH=$arch" "$stage/build-info"
    grep -q "GOOS=linux" "$stage/build-info"
    grep -q "CGO_ENABLED=0" "$stage/build-info"
    test -f "$stage/$arch/LICENSE"
    for script in "$stage/$arch/scripts/"*.sh; do sh -n "$script"; done
    test -f "$stage/$arch/packaging/systemd/connection.conf"
done
test "$("$stage/amd64/bin/kladit-agent" version)" = "kladit-agent $2"
"$stage/amd64/bin/kladit-agent" help > /dev/null
"$stage/amd64/bin/kladit-agent" policy-check --policy /dev/stdin <<'POLICY'
{"version":1,"capabilities":{"metrics.read":true}}
POLICY
