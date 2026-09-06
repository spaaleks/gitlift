#!/usr/bin/env bash
#
# build-binary.sh - build the gitlift Go binary. For local dev and CI.
#
# Usage:
#   bin/build-binary.sh                 build for the host -> ./dist/gitlift
#   bin/build-binary.sh --out PATH      write the binary to PATH
#   bin/build-binary.sh --release       cross-build + archives via goreleaser
#   bin/build-binary.sh --snapshot      goreleaser build without publishing
#
# Environment:
#   VERSION   version string to embed (default: git describe, else "dev")

set -euo pipefail

SELF="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"
cd "$SELF"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="$SELF/dist/gitlift"
MODE="host"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --out) OUT="$2"; shift 2 ;;
        --release) MODE="release"; shift ;;
        --snapshot) MODE="snapshot"; shift ;;
        *) echo "unknown option '$1'" >&2; exit 1 ;;
    esac
done

case "$MODE" in
    host)
        echo "==> building gitlift $VERSION -> $OUT"
        mkdir -p "$(dirname "$OUT")"
        CGO_ENABLED=0 go build \
            -ldflags "-s -w -X github.com/spaaleks/gitlift.Version=$VERSION" \
            -o "$OUT" ./cmd/gitlift
        echo "done: $OUT"
        ;;
    snapshot)
        command -v goreleaser >/dev/null || { echo "goreleaser not installed" >&2; exit 1; }
        goreleaser build --clean --snapshot
        ;;
    release)
        command -v goreleaser >/dev/null || { echo "goreleaser not installed" >&2; exit 1; }
        goreleaser release --clean
        ;;
esac
