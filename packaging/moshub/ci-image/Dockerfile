# MosHub CI runners have no Internet egress (no Debian mirror, no Go proxy):
# everything the suite needs is baked in. Rebuild when go.sum changes.
FROM golang:1.26.0-trixie
RUN apt-get update -qq \
 && apt-get install -y --no-install-recommends python3 python3-yaml sqlite3 passwd util-linux tini \
 && rm -rf /var/lib/apt/lists/*
RUN useradd --create-home --uid 10001 moshub
COPY go.mod go.sum /tmp/mods/
RUN cd /tmp/mods && runuser -u moshub -- env HOME=/home/moshub GOPATH=/home/moshub/go GOFLAGS=-mod=mod go mod download \
 && rm -rf /tmp/mods
ENV GOPATH=/home/moshub/go GOPROXY=off GOFLAGS=-mod=readonly GOTOOLCHAIN=local
