#!/bin/sh
# Remote CI only. The production host installs the verified output.
set -eu
site=$1
project_dir=$(pwd)
worker_dir=$(mktemp -d)
trap 'rm -rf "$worker_dir"' EXIT HUP INT TERM
chmod 755 "$worker_dir"
revision=$(cat .ci/mcpshka-revision)
case "$revision" in *[!0-9a-f]*|'') exit 2;; esac
[ "${#revision}" -eq 40 ] || exit 2
git init -q "$worker_dir/source"
git -C "$worker_dir/source" fetch -q --depth=1 https://github.com/dmitriy1ebedev/mcpshka-ci.git "$revision"
git -C "$worker_dir/source" -c core.hooksPath=/dev/null checkout -q --detach FETCH_HEAD
[ "$(git -C "$worker_dir/source" rev-parse HEAD)" = "$revision" ] || exit 2
export GITHUB_WORKSPACE="$project_dir"
sh "$worker_dir/source/packaging/ci/release_shard.sh" prepare
chown -R ci:ci "$worker_dir"
gobin=$(dirname "$(command -v go)")
as_ci() {
 runuser -u ci -- env HOME=/home/ci PATH="/tmp/project-ci-python/bin:$gobin:/usr/bin:/bin" \
  GOPATH=/home/ci/go GOCACHE=/home/ci/.cache/go-build GOTOOLCHAIN=go1.26.0 \
  GOMAXPROCS=2 GOFLAGS=-mod=readonly SOURCECRAFT_COMMIT_REF="${SOURCECRAFT_COMMIT_REF:-}" "$@"
}
python3 -m venv /tmp/project-ci-python
/tmp/project-ci-python/bin/pip install --disable-pip-version-check PyYAML==6.0.3
for requirements in requirements-control.txt requirements-runtime.txt; do
 if [ -f "$requirements" ]; then /tmp/project-ci-python/bin/pip install --disable-pip-version-check -r "$requirements"; fi
done
(cd "$worker_dir/source" && as_ci go build -p 1 -o "$worker_dir/ci-shard" ./cmd/ci-shard)
as_ci go mod download
if [ "$site" = sourcecraft ] || [ "$site" = sourcecraft-start ]; then
 as_ci "$worker_dir/ci-shard" "$site"
else
 as_ci "$worker_dir/ci-shard" "$CI_SHARD" "$site" "$CI_SOURCE_COMMIT" "$CI_SOURCE_TREE" "$CI_SNAPSHOT" "$CI_PLAN_HASH" "$CI_RUN_ID"
fi
