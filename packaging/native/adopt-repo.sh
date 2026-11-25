#!/bin/sh
# adopt-repo.sh NAME — hand a root-owned /root/repos/NAME Git repository to the
# MCPshka service user so chats can develop it through MCPshka (lease, test,
# fs_write). Run once per repository as root. It changes ownership only; it
# never touches Git history, the working tree contents or other repositories.
set -eu
name=${1:-}
case "$name" in
'' | */* | .* | mcpshka-controlled | 00_*)
	echo "usage: $0 NAME  (an immediate /root/repos repository; protected stores refused)" >&2
	exit 2
	;;
esac
repo=/root/repos/$name
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 2; }
[ -d "$repo/.git" ] || [ -f "$repo/.git" ] || { echo "$repo is not a Git repository" >&2; exit 2; }
[ ! -L "$repo" ] || { echo "$repo is a symlink" >&2; exit 2; }
chown -R --no-dereference mcpshka:mcpshka "$repo"
chmod 2770 "$repo"
echo "adopted $repo: $(stat -c '%U:%G %a' "$repo")"
echo "next: project_onboard_preview path=$repo, then project_onboard"
