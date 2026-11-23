#!/bin/sh
# Rejected tags must fail before creating output or running a compiler.
set -eu
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
for tag in dev 1.2.3 v01.2.3 v1.02.3 v1.2.03 v1.2 v1.2.3-rc.1 'v1.2.3+build' 'v1.2.3 -X main.version=other' 'v1.2.3
v9.0.0'; do
    if sh scripts/package.sh "$stage/out" "https://kladit.example/agent/$tag" "$tag"; then
        echo "Invalid tag was accepted" >&2
        exit 1
    fi
    test ! -e "$stage/out"
done
if sh scripts/package.sh "$stage/out" https://kladit.example/agent/v1.2.4 v1.2.3; then exit 1; fi
if sh scripts/package.sh "$stage/out" https://kladit.example/agent/v1.2.3; then exit 1; fi
test ! -e "$stage/out"
