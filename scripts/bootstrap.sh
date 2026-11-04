#!/bin/sh
# Generated release replaces @BUNDLE_BASE@ with its immutable HTTPS directory.
set -eu
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 077
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo.' >&2; exit 1; }
[ "$#" -eq 2 ] && [ "$1" = --enroll-url ] || { echo 'Expected --enroll-url HTTPS_URL' >&2; exit 1; }
[ "$(uname -s)" = Linux ] || exit 1
case "$(uname -m)" in
 x86_64) arch=amd64 ;;
 aarch64|arm64) arch=arm64 ;;
 *) echo 'Supported architectures: x86_64, arm64' >&2; exit 1 ;;
esac
for cmd in curl sha256sum tar mktemp; do command -v "$cmd" >/dev/null || exit 1; done
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
base='@BUNDLE_BASE@'
name=kladit-agent-linux-$arch.tar.gz
curl --fail --silent --show-error --proto '=https' --tlsv1.2 "$base/$name" -o "$stage/$name"
curl --fail --silent --show-error --proto '=https' --tlsv1.2 "$base/$name.sha256" -o "$stage/$name.sha256"
(cd "$stage" && sha256sum --check "$name.sha256" >/dev/null)
tar -xzf "$stage/$name" -C "$stage"
sh "$stage/scripts/install.sh" --allow-metrics --enroll-url "$2"
