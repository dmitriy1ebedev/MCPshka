#!/bin/sh
# One shard of the MCPshka release suite on a CI runner (GitVerse
# snapshot-contracts.yml, 0.9.8 «шардирование»): shard 0 is the heavy
# internal/mcpserver package (+ race test), shards 1 and 2 split the rest.
# Runs as root in the CI container: prepares Git 2.44+ and tini as on H96,
# then vets and tests unprivileged as user ci.
set -eu
shard=$1
apt-get update -qq
apt-get install -y --no-install-recommends tini python3-venv
# GitVerse runner images currently vary between Git 2.43 and 2.56.
# The sandbox uses --no-lazy-fetch; install pinned upstream Git on older
# images without relying on the intermittently unavailable git-core PPA.
if ! git --no-lazy-fetch version >/dev/null 2>&1; then
  apt-get install -y --no-install-recommends build-essential ca-certificates curl \
    xz-utils zlib1g-dev libssl-dev libcurl4-openssl-dev libexpat1-dev
  git_tmp="$(mktemp -d)"
  trap 'rm -rf "$git_tmp"' EXIT HUP INT TERM
  curl --fail --silent --show-error --location --retry 3 \
    --connect-timeout 10 --max-time 150 \
    https://www.kernel.org/pub/software/scm/git/git-2.47.3.tar.xz \
    -o "$git_tmp/git-2.47.3.tar.xz"
  (cd "$git_tmp" && printf '%s  %s\n' \
    '9c2eb1250781b3e5bfef098572d07fdf132d67e6c065e4307332ade9819a1501' \
    git-2.47.3.tar.xz | sha256sum -c -)
  tar -xJf "$git_tmp/git-2.47.3.tar.xz" -C "$git_tmp"
  (cd "$git_tmp/git-2.47.3" && \
    make -j2 NO_GETTEXT=YesPlease NO_TCLTK=YesPlease NO_PERL=YesPlease \
      NO_PYTHON=YesPlease prefix=/usr all && \
    make NO_GETTEXT=YesPlease NO_TCLTK=YesPlease NO_PERL=YesPlease \
      NO_PYTHON=YesPlease prefix=/usr install)
fi
/usr/bin/git --no-lazy-fetch version >/dev/null || {
  echo "::error::Git 2.44+ unavailable after verified bootstrap"; exit 1; }
id ci >/dev/null 2>&1 || useradd --create-home --uid 10001 ci
chown -R ci:ci "$GITHUB_WORKSPACE"
gobin="$(dirname "$(command -v go)")"
as_ci() { runuser -u ci -- env -i PATH="$gobin:/usr/bin:/bin" HOME=/home/ci LANG=C \
  GOCACHE=/home/ci/.cache/go-build GOMODCACHE=/home/ci/go/pkg/mod GOFLAGS=-mod=readonly "$@"; }
[ "$shard" != prepare ] || exit 0
all=$(as_ci go list ./...)
heavy=mcpshka/internal/mcpserver
case $shard in
  0) pkgs=$heavy ;;
  1|2) pkgs=$(printf '%s\n' "$all" | grep -vx "$heavy" | awk -v s="$shard" 'NR % 2 == s - 1') ;;
  *) echo "shard must be 0, 1 or 2"; exit 2 ;;
esac
# shellcheck disable=SC2086
as_ci go vet $pkgs
# shellcheck disable=SC2086
as_ci /usr/bin/tini -s -g -- go test -p 2 -count=1 $pkgs
if [ "$shard" = 0 ]; then
  as_ci /usr/bin/tini -s -g -- go test -race -p 1 -count=1 -run 'TestDynamic' ./internal/mcpserver
fi
