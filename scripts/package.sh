#!/bin/sh
# Release operator builds once; users never need Go or git.
set -eu
[ "$#" -eq 2 ] || { echo 'Usage: sh scripts/package.sh OUTPUT_DIR HTTPS_BUNDLE_BASE' >&2; exit 1; }
case "$2" in https://*) ;; *) exit 1 ;; esac
case "$2" in *[!a-zA-Z0-9:/._-]*) echo 'Invalid bundle base' >&2; exit 1 ;; esac
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$1"
out=$(CDPATH= cd -- "$1" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
cd "$root"
mkdir -p "$stage/bin" "$stage/scripts" "$stage/packaging"
cp scripts/install.sh scripts/uninstall.sh "$stage/scripts/"
cp -R packaging/systemd "$stage/packaging/"
cp LICENSE "$stage/"
for arch in amd64 arm64; do
 CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -o "$stage/bin/kladit-agent" ./cmd/kladit-agent
 name=kladit-agent-linux-$arch.tar.gz
 tar -czf "$out/$name" -C "$stage" bin scripts packaging LICENSE
 (cd "$out" && sha256sum "$name" > "$name.sha256")
done
sed "s|@BUNDLE_BASE@|$2|g" scripts/bootstrap.sh > "$out/install.sh"
