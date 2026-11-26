#!/bin/sh
# First registration: curl -fsSL @ORIGIN@/runner/setup | sh -s TOKEN
# Rerun without TOKEN to use the existing credential.
set -eu
umask 077
U='@ORIGIN@'
D=$HOME
for d in /workspaces "$HOME/workspace"; do
  if [ -d "$d" ] && [ -w "$d" ]; then D=$d; break; fi
done
D=$D/.mcpshka-runner
mkdir -p "$D"
echo '*' >"$D/.gitignore"
[ -f "$D/key.json" ] || [ ! -f "$HOME/.mcpshka-runner.json" ] || cp "$HOME/.mcpshka-runner.json" "$D/key.json"
printf '%s\n' "$U" >"$D/origin"
start=$(mktemp "$D/.start.XXXXXX")
trap 'rm -f "$start"' EXIT
cat >"$start" <<'START'
#!/bin/sh
set -eu
umask 077
D=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
U=$(cat "$D/origin")
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) arch=amd64 ;;
  Linux:aarch64|Linux:arm64) arch=arm64 ;;
  *) echo "MCPshka runner requires Linux amd64 or arm64" >&2; exit 1 ;;
esac
tmp=$(mktemp "$D/.runner.XXXXXX")
trap 'rm -f "$tmp"' EXIT
curl -fsSL --connect-timeout 10 --max-time 120 --retry 2 "$U/runner/download/linux-$arch" -o "$tmp"
chmod 700 "$tmp"
mv "$tmp" "$D/runner"
nohup "$D/runner" --url "$U" --conf "$D/key.json" "$@" >>"$D/log" 2>&1 &
START
chmod 700 "$start"
mv "$start" "$D/start"
if [ "${1:-}" = --start ]; then
  exec sh "$D/start"
fi
rc=$HOME/.bashrc
grep -qs '.mcpshka-runner/start' "$rc" || echo "sh '$D/start' >/dev/null 2>&1" >>"$rc"
if [ -n "${1:-}" ]; then sh "$D/start" --token "$1"; else sh "$D/start"; fi

# The folder-open task starts the runner when no terminal is restored.
mkdir -p "$D/keeper"
cat >"$D/keeper.code-workspace" <<'WORKSPACE'
{
  "folders": [{"path": "keeper"}],
  "settings": {"task.allowAutomaticTasks": "on"},
  "tasks": {"version": "2.0.0", "tasks": [{
    "label": "MCPshka pull runner",
    "type": "process",
    "command": "/bin/sh",
    "args": ["${workspaceFolder}/../start"],
    "problemMatcher": [],
    "runOptions": {"runOn": "folderOpen"},
    "presentation": {"reveal": "silent", "panel": "dedicated"}
  }]}
}
WORKSPACE
echo "Автозапуск IDE: откройте $D/keeper.code-workspace и разрешите автоматические задачи"
echo "Журнал раннера: $D/log"
