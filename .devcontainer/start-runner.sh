#!/bin/sh
set -eu
origin=${MCPSHKA_URL:-}
for root in /workspaces "$HOME/workspace" "$HOME"; do
  dir=$root/.mcpshka-runner
  if [ -z "$origin" ] && [ -f "$dir/origin" ]; then
    origin=$(cat "$dir/origin")
  fi
  if [ -z "$origin" ] && [ -f "$dir/key.json" ]; then
    # Migrate credentials written by the Python runner (one-line JSON).
    origin=$(sed -n 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$dir/key.json")
  fi
  [ -z "$origin" ] || break
done
if [ -z "$origin" ] && [ -f "$HOME/.mcpshka-runner.json" ]; then
  origin=$(sed -n 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$HOME/.mcpshka-runner.json")
fi
[ -n "$origin" ] || exit 0
installer=$(mktemp)
trap 'rm -f "$installer"' EXIT
curl -fsSL --connect-timeout 10 --max-time 30 --retry 2 "${origin%/}/runner/setup" -o "$installer"
sh "$installer" --start
