#!/bin/sh
# MCPshka runner setup — one line in any cloud IDE terminal:
#   curl -fsSL @ORIGIN@/runner/setup | sh -s TOKEN   (first time)
#   curl -fsSL @ORIGIN@/runner/setup | sh            (again, key is kept)
# The key and agent live where the IDE keeps files across restarts
# (/workspaces, ~/workspace, else ~); autostart goes to ~/.bashrc.
# The runner exits by itself after 10 min without jobs.
set -e
U='@ORIGIN@'
D=$HOME
for d in /workspaces "$HOME/workspace"; do
  if [ -d "$d" ] && [ -w "$d" ]; then D=$d; break; fi
done
D=$D/.mcpshka-runner
mkdir -p "$D"
echo '*' >"$D/.gitignore"
[ -f "$D/key.json" ] || [ ! -f "$HOME/.mcpshka-runner.json" ] || cp "$HOME/.mcpshka-runner.json" "$D/key.json"
cat >"$D/start" <<S
#!/bin/sh
pgrep -f "$D/runner.py" >/dev/null 2>&1 && exit 0
curl -fsSL "$U/runner/agent.py" -o "$D/runner.new" && mv "$D/runner.new" "$D/runner.py"
nohup python3 "$D/runner.py" --url "$U" --conf "$D/key.json" "\$@" >>"$D/log" 2>&1 &
S
chmod 700 "$D/start"
pkill -f 'mcpshka-runner.py' 2>/dev/null || true
pkill -f "$HOME/runner --url" 2>/dev/null || true
rc=$HOME/.bashrc
[ -f "$rc" ] && sed -i '\#bash ~/up#d' "$rc"
grep -qs '.mcpshka-runner/start' "$rc" || echo "sh '$D/start' >/dev/null 2>&1" >>"$rc"
if [ -n "$1" ]; then sh "$D/start" --token "$1"; else sh "$D/start"; fi
# A folder-open task starts a terminal even when the IDE restores none.
mkdir -p "$D/keeper"
python3 - "$D" <<'PY'
import json
import os
import sys
d = sys.argv[1]
task = {"label": "MCPshka pull runner", "type": "process", "command": "/bin/sh",
        "args": [os.path.join(d, "start")], "problemMatcher": [],
        "runOptions": {"runOn": "folderOpen"},
        "presentation": {"reveal": "silent", "panel": "dedicated"}}
with open(os.path.join(d, "keeper.code-workspace"), "w") as f:
    json.dump({"folders": [{"path": "keeper"}],
               "settings": {"task.allowAutomaticTasks": "on"},
               "tasks": {"version": "2.0.0", "tasks": [task]}}, f)
PY
echo "Автозапуск IDE: откройте $D/keeper.code-workspace и разрешите автоматические задачи"
sleep 5
tail -n 3 "$D/log"
echo "MCPshka runner: $D (autostart on)"
