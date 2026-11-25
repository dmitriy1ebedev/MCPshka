#!/bin/sh
# Подключает MCPshka к локальному Codex (CLI и app-server). Токен не печатается.
set -e
cfg=/root/.codex/config.toml
cp -p "$cfg" "$cfg.pre-mcpshka-20261020.bak"
python3 - "$cfg" <<'EOF'
import sys
p = sys.argv[1]
s = open(p).read()
if '[mcp_servers.mcpshka]' in s:
    print('MCPshka уже подключена'); raise SystemExit
v = open('/etc/mcpshka/mcp-auth-header').read().strip()
if not v.startswith('Bearer '):
    v = 'Bearer ' + v
s += ('\n[mcp_servers.mcpshka]\nurl = "http://127.0.0.1:8765/mcp"\n'
      'http_headers = { Authorization = "%s" }\n'
      'startup_timeout_sec = 30\ntool_timeout_sec = 300\n' % v)
open(p, 'w').write(s)
print('MCPshka добавлена в', p)
EOF
chmod 600 "$cfg"
codex mcp list 2>&1 | grep -E '^Name|^mcpshka'
