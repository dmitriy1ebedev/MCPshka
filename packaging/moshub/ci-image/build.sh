#!/bin/bash
# Build the offline MosHub CI image and push it to the project registry;
# prints the digest to set as the MosHub CI variable MOSHUB_CI_IMAGE.
# For a host with a docker daemon. GitVerse CI uses assemble.sh instead:
# its runners forbid user namespaces, so no RUN-based build works there.
# MosHub runners are x86, so PLATFORMS defaults to linux/amd64.
set -euo pipefail
cd "$(dirname "$0")/../../.."
IMAGE=registry-hub.mos.ru/lda6994/mcpshka-ci/ci
TAG=go1.26-$(sha256sum go.sum | cut -c1-12)
ctx=$(mktemp -d); trap 'rm -rf "$ctx"' EXIT
cp go.mod go.sum packaging/moshub/ci-image/Dockerfile "$ctx/"
if docker info >/dev/null 2>&1; then
  docker buildx build --platform "${PLATFORMS:-linux/amd64}" -t "$IMAGE:$TAG" --push "$ctx"
  docker buildx imagetools inspect "$IMAGE:$TAG" --format '{{.Manifest.Digest}}' | sed "s|^|$IMAGE@|"
else
  # GitVerse runners have the docker CLI but no daemon: build daemonless.
  # buildah reads the registry login from REGISTRY_AUTH_FILE (docker login
  # wrote it to ~/.docker/config.json).
  export REGISTRY_AUTH_FILE=${REGISTRY_AUTH_FILE:-$HOME/.docker/config.json}
  B="buildah --storage-driver vfs"
  $B bud --isolation chroot --platform "${PLATFORMS:-linux/amd64}" -t "$IMAGE:$TAG" "$ctx" >&2
  digest=$(mktemp)
  $B push --digestfile "$digest" "$IMAGE:$TAG" >&2
  echo "$IMAGE@$(cat "$digest")"
fi
