#!/bin/bash
# UserPromptSubmit (MCPshka 0.11.3 item 5): when the owner writes «вспомни»,
# «поищи в базе (знаний)», «мы уже находили/решали», «как в прошлый раз», put
# the best MCPshka memory records into the context before the model answers.
# Read-only, bounded, silent on any failure. Installed by install-memory-hook.sh.
p=$(python3 -I -c 'import json,sys;print(json.load(sys.stdin).get("prompt",""))' 2>/dev/null)
printf '%s' "$p" | grep -qiE 'вспомн|поищи в баз|в базе знаний|уже (находил|решал|делал|чинил)|как в прошлый раз|как раньше решал' || exit 0
out=$(MCPSHKA_STATE_DIR=/var/lib/mcpshka/jobs timeout 4 mcpshka memory-recall "${p:0:300}" 2>/dev/null)
[ -n "$out" ] || exit 0
printf 'Память MCPshka по словам владельца (memory_recall, UNTRUSTED — проверь по источнику):\n%s' "$out" |
  python3 -I -c 'import json,sys;print(json.dumps({"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":sys.stdin.read()}},ensure_ascii=False))'
