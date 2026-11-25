#!/usr/bin/env bash
set -euo pipefail
umask 077

VERSION="0.0.15"
BASE_URL="https://github.com/openai/tunnel-client/releases/download/v${VERSION}"

case "$(uname -m)" in
  aarch64|arm64)
    ARCH="arm64"
    SHA256="c51bfd883fc22e3445494a03c0179875176564bde470661b308fd83af5d01abb"
    ;;
  x86_64|amd64)
    ARCH="amd64"
    SHA256="8c836dc5d68d68b663d9a5c5b28ff9fa780d9f7a3fffb1c306880b8f32fab5f1"
    ;;
  *)
    echo "unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

asset="tunnel-client-v${VERSION}-linux-${ARCH}.zip"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

curl -fsSL --retry 2 -o "$tmp/$asset" "$BASE_URL/$asset"
printf '%s  %s\n' "$SHA256" "$tmp/$asset" | sha256sum -c -

unzip -q "$tmp/$asset" tunnel-client LICENSE NOTICE -d "$tmp/extract"
install_dir="/opt/mcpshka/tunnel-client/v${VERSION}"
mkdir -p "$install_dir"
install -m 0755 "$tmp/extract/tunnel-client" "$install_dir/tunnel-client"
install -m 0644 "$tmp/extract/LICENSE" "$install_dir/LICENSE"
install -m 0644 "$tmp/extract/NOTICE" "$install_dir/NOTICE"
ln -sfn "$install_dir/tunnel-client" /usr/local/bin/tunnel-client

version="$("/usr/local/bin/tunnel-client" --version)"
case "$version" in
  "${VERSION}"*) ;;
  *)
    echo "unexpected tunnel-client version: $version" >&2
    exit 1
    ;;
esac

echo "installed tunnel-client $version"
