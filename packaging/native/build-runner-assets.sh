#!/bin/sh
set -eu
out=${1:-internal/runnerhub/assets}
mkdir -p "$out"
scratch=$(mktemp -d "$out/.build.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
for arch in amd64 arm64; do
    GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -p 2 -trimpath -buildvcs=false -ldflags="-s -w" -o "$scratch/runner" ./cmd/mcpshka-runner
    gzip -n -c "$scratch/runner" >"$scratch/linux-$arch.gz"
done
for arch in amd64 arm64; do
    mv "$scratch/linux-$arch.gz" "$out/"
done
