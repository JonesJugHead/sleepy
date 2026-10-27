#!/bin/sh
# Builds dist/sleepy-v<VERSION>: static Linux x64 binary (GOAMD64=v1, runs on any x86-64 CPU).
# Uses a local Go toolchain if present, otherwise the golang Docker image.
set -e
cd "$(dirname "$0")"
OUT="dist/sleepy-v$(cat VERSION)"
# timetzdata embeds the time zone database: TZ works where the system has none (sleepy.json dates).
BUILD="go build -trimpath -tags timetzdata -ldflags='-s -w' -o $OUT ."
if command -v go >/dev/null 2>&1; then
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 sh -c "$BUILD"
else
  SRC="$(pwd -W 2>/dev/null || pwd)"
  MSYS_NO_PATHCONV=1 docker run --rm -v "$SRC":/src -v sleepy-gocache:/root/.cache -v sleepy-gomod:/go/pkg/mod -w /src \
    -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 -e GOAMD64=v1 golang:1.25 sh -c "$BUILD"
fi
echo "Built $OUT ($(wc -c < "$OUT") bytes)"
