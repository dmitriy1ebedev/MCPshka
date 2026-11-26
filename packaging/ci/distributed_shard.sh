#!/bin/sh
# Fixed CI entry point: bootstrap once, then run the source-bound Go shard.
set -eu
site=$1
command -v go >/dev/null || { echo "CI infrastructure: Go toolchain missing from PATH"; exit 2; }
: "${CI_RUN_ID:?CI infrastructure: not passed}"
if [ -z "${CI_PUBLIC_REF:-}" ]; then
  : "${CI_SHARD:?CI infrastructure: not passed}" "${CI_SOURCE_COMMIT:?CI infrastructure: not passed}" \
    "${CI_SOURCE_TREE:?CI infrastructure: not passed}" "${CI_SNAPSHOT:?CI infrastructure: not passed}" \
    "${CI_PLAN_HASH:?CI infrastructure: not passed}"
fi
: "${GITHUB_WORKSPACE:=$(pwd)}"
export GITHUB_WORKSPACE
if [ "$site" = moshub ] && [ -z "${CI_PUBLIC_REF:-}" ]; then
  account=moshub
  git --no-lazy-fetch version >/dev/null || { echo "CI infrastructure: Git 2.44+ missing"; exit 2; }
  command -v tini >/dev/null || { echo "CI infrastructure: tini missing"; exit 2; }
else
  sh packaging/ci/release_shard.sh prepare
  account=ci
  python3 -m venv /tmp/mcpshka-ci-python
  /tmp/mcpshka-ci-python/bin/python -m pip install --disable-pip-version-check -r packaging/native/requirements-cloud.txt
fi
gobin=$(dirname "$(command -v go)")
as_ci() {
  runuser -u "$account" -- env -i HOME="/home/$account" PATH="/tmp/mcpshka-ci-python/bin:$gobin:/usr/bin:/bin" \
    GOPATH="/home/$account/go" GOCACHE="/home/$account/.cache/go-build" GOFLAGS=-mod=readonly \
    GOMAXPROCS=2 GOMEMLIMIT=1200MiB "$@"
}
# Go list runs offline after this download.
as_ci python3 -c 'import yaml' || { echo "CI infrastructure: PyYAML unavailable"; exit 2; }
as_ci go mod download || { echo "CI infrastructure: module cache unavailable"; exit 2; }
if [ -n "${CI_PUBLIC_REF:-}" ]; then
  as_ci tini -s -g -- go run ./cmd/ci-shard push "$site" "$CI_PUBLIC_REF" "$CI_RUN_ID"
  exit
fi
as_ci tini -s -g -- go run ./cmd/ci-shard "$CI_SHARD" "$site" "$CI_SOURCE_COMMIT" \
  "$CI_SOURCE_TREE" "$CI_SNAPSHOT" "$CI_PLAN_HASH" "$CI_RUN_ID"
