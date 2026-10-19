#!/bin/sh
# Builds the release archives and checksums.txt the installer downloads.
#
#   scripts/dist.sh [out-dir] [os/arch ...]
#
# Defaults: ./dist and every platform the installer supports.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$root/dist}
[ $# -gt 0 ] && shift
targets=${*:-darwin/arm64 darwin/amd64 linux/arm64 linux/amd64}

mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for t in $targets; do
  os=${t%/*}
  arch=${t#*/}
  mkdir -p "$work/$os-$arch"
  (cd "$root" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$work/$os-$arch/credence" ./cmd/credence)
  cp "$root/LICENSE" "$root/README.md" "$work/$os-$arch/"
  tar -czf "$out/credence_${os}_${arch}.tar.gz" -C "$work/$os-$arch" credence LICENSE README.md
  echo "built credence_${os}_${arch}.tar.gz"
done

(cd "$out" && for f in credence_*.tar.gz; do
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$f"; else shasum -a 256 "$f"; fi
done) > "$out/checksums.txt"
echo "wrote $out/checksums.txt"
