#!/usr/bin/env bash
#
# run.sh - run gitlift straight from source, without building a binary.
#          For local development; use bin/build-binary.sh to ship one.
#
# Usage:
#   bin/run.sh                  start the interface
#   bin/run.sh --paths          any gitlift flag is passed through
#
# Runs from the repository root, so ./.providers.yaml and ./templates are the
# ones picked up.
#
# Environment:
#   VERSION   version string to embed (default: git describe, else "dev")

set -euo pipefail

SELF="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"
cd "$SELF"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

exec go run \
    -ldflags "-X github.com/spaaleks/gitlift.Version=$VERSION" \
    ./cmd/gitlift "$@"
