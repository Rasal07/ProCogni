#!/bin/sh
# Runs the Go toolchain in Docker, for machines without Go installed.
#   scripts/go.sh test ./...
#   scripts/go.sh run ./cmd/mock        (add PORTS="-p 8080:8080" to reach it)
cd "$(dirname "$0")/.." || exit 1
MSYS_NO_PATHCONV=1 exec docker run --rm $PORTS \
  -v "$(pwd -W 2>/dev/null || pwd):/src" \
  -v proleaf-gomod:/go/pkg/mod -v proleaf-gocache:/root/.cache/go-build \
  -w /src golang:1.25-alpine go "$@"
