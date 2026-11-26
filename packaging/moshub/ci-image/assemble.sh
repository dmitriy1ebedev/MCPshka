#!/bin/bash
# Assemble the offline MosHub CI image without building it: no RUN step, so
# no user namespaces. GitVerse runners run inside a user namespace and
# forbid unshare(CLONE_NEWUSER) even for root, so buildah fails there
# (runs 1997283, 1997867; BUG-20261103-2). Base golang image + one appended
# layer (Debian Debian packages extracted, the moshub user, the Go module
# cache), pushed with crane. Prints the pinned reference for the MosHub CI
# variable MOSHUB_CI_IMAGE. Needs: root (apt resolver, chown), go, crane,
# dpkg-deb, debian-archive-keyring, registry login in $DOCKER_CONFIG.
set -euo pipefail
cd "$(dirname "$0")/../../.."
IMAGE=${IMAGE:-registry-hub.mos.ru/lda6994/mcpshka-ci/ci}
# trixie: git 2.47 (MCPshka passes --no-lazy-fetch, git >= 2.44; bookworm's
# 2.39 exits 129 in every snapshot test, MosHub pipeline 92941).
SUITE=${SUITE:-trixie}
BASE=${BASE:-index.docker.io/library/golang:1.26.0-$SUITE}
PLATFORM=linux/amd64
PKGS="python3 python3-yaml sqlite3 tini"
TAG=go1.26-$(cat go.sum packaging/moshub/ci-image/assemble.sh | sha256sum | cut -c1-12)
w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
root=$w/root
mkdir -p "$root/etc" "$w/base" "$w/apt/lists/partial" "$w/apt/archives/partial"

# The amd64 manifest of the base, pinned by digest.
base="${BASE%:*}@$(crane digest --platform "$PLATFORM" "$BASE")"
echo "base: $base" >&2

# 1. What the base already has: dpkg status and account files.
crane export "$base" - | tar -x -C "$w/base" --wildcards \
  '*var/lib/dpkg/status' '*etc/passwd' '*etc/group' '*etc/shadow'
b=$(dirname "$(find "$w/base" -path '*var/lib/dpkg/status' | head -1)")/../../..
b=$(cd "$b" && pwd)

# 2. The missing Debian packages, resolved against that status. apt only
#    resolves and downloads; nothing is installed on the runner.
kr=/usr/share/keyrings/debian-archive-keyring.gpg
cat > "$w/sources.list" <<S
deb [arch=amd64 signed-by=$kr] http://deb.debian.org/debian $SUITE main
deb [arch=amd64 signed-by=$kr] http://deb.debian.org/debian $SUITE-updates main
deb [arch=amd64 signed-by=$kr] http://deb.debian.org/debian-security $SUITE-security main
S
apt_() {
  apt-get -q -o Dir::Etc::SourceList="$w/sources.list" -o Dir::Etc::SourceParts=- \
    -o Dir::Etc::Preferences=- -o Dir::Etc::PreferencesParts=- \
    -o Dir::State="$w/apt" -o Dir::State::Lists="$w/apt/lists" \
    -o Dir::State::status="$b/var/lib/dpkg/status" \
    -o Dir::Cache="$w/apt" -o Dir::Cache::archives="$w/apt/archives" \
    -o APT::Architecture=amd64 -o APT::Architectures::=amd64 \
    -o APT::Sandbox::User=root -o Debug::NoLocking=1 "$@"
}
apt_ update >&2
apt_ install -y --download-only --no-install-recommends $PKGS >&2
ls "$w"/apt/archives/*.deb >&2
for d in "$w"/apt/archives/*.deb; do dpkg-deb -x "$d" "$root"; done
# The base is merged-/usr (/bin -> usr/bin): a real /bin in the layer would
# replace that symlink and hide the base's binaries.
for d in bin sbin lib lib32 lib64 libx32; do
  if [ -d "$root/$d" ] && [ ! -L "$root/$d" ]; then
    mkdir -p "$root/usr/$d"; cp -a "$root/$d/." "$root/usr/$d/"; rm -rf "${root:?}/$d"
  fi
done
test -x "$root/usr/bin/sqlite3" && test -e "$root/usr/bin/python3" && test -x "$root/usr/bin/tini" && test -d "$root/usr/lib/python3/dist-packages/yaml"

# 3. The unprivileged test user (uid 10001), as useradd would write it.
cp -p "$b/etc/passwd" "$b/etc/group" "$b/etc/shadow" "$root/etc/"
grep -q '^moshub:' "$root/etc/passwd" || {
  echo 'moshub:x:10001:10001::/home/moshub:/bin/sh' >> "$root/etc/passwd"
  echo 'moshub:x:10001:' >> "$root/etc/group"
  echo 'moshub:!:20000:0:99999:7:::' >> "$root/etc/shadow"
}

# 4. Go module sources (platform independent), owned by moshub.
mkdir -p "$root/home/moshub/go"
env GOPATH="$root/home/moshub/go" GOMODCACHE="$root/home/moshub/go/pkg/mod" \
  GOFLAGS=-mod=mod GOTOOLCHAIN=local go mod download >&2
chown -R 10001:10001 "$root/home/moshub"

# 5. One layer on the pinned base, environment as the Dockerfile set it.
(cd "$root" && tar --numeric-owner -cf "$w/layer.tar" .)
crane append -b "$base" -f "$w/layer.tar" -t "$IMAGE:$TAG-layer" >&2
crane mutate "$IMAGE:$TAG-layer" -t "$IMAGE:$TAG" \
  --env GOPATH=/home/moshub/go --env GOPROXY=off \
  --env GOFLAGS=-mod=readonly --env GOTOOLCHAIN=local >&2
digest=$(crane digest "$IMAGE:$TAG")
# An empty digest once printed "…/ci@" and the run stayed green (run 2000471).
case $digest in sha256:????????????????????????????????????????????????????????????????) ;; *) echo "no digest for $IMAGE:$TAG" >&2; exit 1;; esac
echo "$IMAGE@$digest"
