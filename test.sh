#!/bin/sh
# Runs the test suite on Linux (the backend tests need sh and process groups).
set -e
cd "$(dirname "$0")"
if command -v go >/dev/null 2>&1 && [ "$(go env GOOS)" = linux ]; then
  go vet ./... && go test "$@" ./...
else
  SRC="$(pwd -W 2>/dev/null || pwd)"
  MSYS_NO_PATHCONV=1 docker run --rm -v "$SRC":/src -v sleepy-gocache:/root/.cache -v sleepy-gomod:/go/pkg/mod -w /src \
    golang:1.25 sh -c "go vet ./... && go test $* ./..."
fi
