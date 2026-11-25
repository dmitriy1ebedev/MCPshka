#!/bin/sh
# setup keeps the credential in the workspace across IDE sleeps.
set -eu
for root in /workspaces "$HOME/workspace" "$HOME"; do
  if [ -f "$root/.mcpshka-runner/start" ]; then
    exec sh "$root/.mcpshka-runner/start"
  fi
done
conf=$HOME/.mcpshka-runner.json
origin=${MCPSHKA_URL:-}
if [ -z "$origin" ] && [ -f "$conf" ]; then
  origin=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["url"])' "$conf")
fi
[ -n "$origin" ] || exit 0
curl -fsSL "$origin/runner/agent.py" -o "$HOME/mcpshka-runner.py"
nohup python3 "$HOME/mcpshka-runner.py" --url "$origin" > "$HOME/mcpshka-runner.log" 2>&1 &
