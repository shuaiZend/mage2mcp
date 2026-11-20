#!/usr/bin/env bash
# Build every release binary for every supported platform.
#
# Used by .github/workflows/release.yml and release-dry-run.yml so the release
# path and its dry run cannot drift apart.
set -euo pipefail

VERSION="${VERSION:-dev}"
OUTDIR="${OUTDIR:-dist}"
TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64}"
COMMANDS="${COMMANDS:-server worker admin}"

mkdir -p "$OUTDIR"

for target in $TARGETS; do
  os="${target%/*}"
  arch="${target#*/}"
  for cmd in $COMMANDS; do
    out="$OUTDIR/mage2mcp-${cmd}_${os}_${arch}"
    if [ "$os" = "windows" ]; then out="${out}.exe"; fi
    echo "building $out"
    GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o "$out" "./cmd/${cmd}"
  done
done

echo "built $(find "$OUTDIR" -maxdepth 1 -type f | wc -l | tr -d ' ') artifacts in $OUTDIR"
