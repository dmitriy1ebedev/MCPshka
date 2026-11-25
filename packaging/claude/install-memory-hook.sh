#!/bin/bash
# Installs the MCPshka memory hook into Claude Code (UserPromptSubmit): the
# owner's «вспомни / поищи в базе» puts recalled records into the context.
# Rollback: cp ~/.claude/settings.json.bak-memory-hook ~/.claude/settings.json
set -e
here=$(dirname "$(readlink -f "$0")")
install -m 755 "$here/mcpshka-memory-recall.sh" /root/.claude/hooks/mcpshka-memory-recall.sh
cp /root/.claude/settings.json /root/.claude/settings.json.bak-memory-hook
python3 -I - <<'PY'
import json
p = '/root/.claude/settings.json'
d = json.load(open(p))
d.setdefault('hooks', {})['UserPromptSubmit'] = [{"hooks": [{"type": "command", "command": "/root/.claude/hooks/mcpshka-memory-recall.sh", "timeout": 6}]}]
json.dump(d, open(p, 'w'), ensure_ascii=False, indent=2)
PY
echo '{"prompt":"вспомни как управлять устройствами дома"}' | /root/.claude/hooks/mcpshka-memory-recall.sh | head -c 300; echo
echo "хук памяти установлен (откат: cp ~/.claude/settings.json.bak-memory-hook ~/.claude/settings.json)"
