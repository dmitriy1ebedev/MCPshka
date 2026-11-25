#!/bin/bash
# GigaIDE Cloud worker for MCPshka (runs INSIDE the workspace, never on H96).
#
# H96 queues a job by pushing an immutable snapshot commit to
# refs/heads/gigaide-jobs/<source sha>; this worker runs the full suite on
# the 4-CPU x86 workspace and pushes a parentless result commit to
# refs/heads/gigaide-results/<source sha>. Nothing is ever force-pushed.
#
# The free allowance turns into paid billing automatically when it runs out,
# and the owner forbids paid use: every minute the worker is up is recorded
# in the gigaide-ledger branch (usage-YYYY-MM.txt) and the worker refuses to
# start, or stops, at GIGAIDE_BUDGET_MIN (default 3000 = 50 h of ~60 h). It
# exits after GIGAIDE_IDLE_MIN idle minutes; close the browser tab after
# that so the workspace scales to zero.
set -uo pipefail
# Only inside GigaIDE (Knative): the folder-open task must not run elsewhere.
if [ -z "${K_SERVICE:-}" ] && [ "${GIGAIDE_FORCE:-}" != 1 ]; then
  echo "not a GigaIDE workspace (no K_SERVICE): worker not started"
  exit 0
fi
BUDGET_MIN=${GIGAIDE_BUDGET_MIN:-3000}
IDLE_MIN=${GIGAIDE_IDLE_MIN:-15}
ROOT=$(git rev-parse --show-toplevel) || exit 1
cd "$ROOT" || exit 1
STATE=${XDG_STATE_HOME:-$HOME/.local/state}/mcpshka-gigaide
mkdir -p "$STATE"
month=$(date -u +%Y-%m)
started=$(date +%s)
last_work=$started
recorded=0

log() { echo "[gigaide $(date -u +%H:%M:%S)] $*"; }

fetch() {
  git fetch -q origin '+refs/heads/gigaide-jobs/*:refs/remotes/origin/gigaide-jobs/*' \
    '+refs/heads/gigaide-results/*:refs/remotes/origin/gigaide-results/*' 2>/dev/null
  git fetch -q origin '+refs/heads/gigaide-ledger:refs/remotes/origin/gigaide-ledger' 2>/dev/null || true
}

# Minutes already used this month according to the shared ledger.
used_minutes() {
  git show "refs/remotes/origin/gigaide-ledger:usage-$month.txt" 2>/dev/null | awk '{s+=$2} END {print s+0}'
}

# Append this session's not yet recorded minutes; retried on a race.
record() {
  local now minutes
  now=$(date +%s)
  minutes=$(( (now - started) / 60 - recorded ))
  [ "$minutes" -le 0 ] && return 0
  for _ in 1 2 3; do
    fetch
    local parent file tree blob commit
    parent=$(git rev-parse -q --verify refs/remotes/origin/gigaide-ledger) || parent=
    file=$( { [ -n "$parent" ] && git show "$parent:usage-$month.txt" 2>/dev/null; echo "$(date -u +%FT%TZ) $minutes ${HOSTNAME:-workspace}"; } )
    blob=$(printf '%s\n' "$file" | git hash-object -w --stdin)
    if [ -n "$parent" ]; then
      tree=$( { git ls-tree "$parent" | grep -v "	usage-$month.txt$"; printf '100644 blob %s\tusage-%s.txt\n' "$blob" "$month"; } | git mktree)
      commit=$(git commit-tree "$tree" -p "$parent" -m "usage $month +$minutes min")
    else
      tree=$(printf '100644 blob %s\tusage-%s.txt\n' "$blob" "$month" | git mktree)
      commit=$(git commit-tree "$tree" -m "usage $month +$minutes min")
    fi
    if git push -q origin "$commit:refs/heads/gigaide-ledger" 2>/dev/null; then
      recorded=$(( recorded + minutes ))
      return 0
    fi
  done
  log "ledger update failed; stopping to stay inside the free allowance"
  return 1
}

over_budget() {
  local used=$(( $(used_minutes) + ( $(date +%s) - started ) / 60 - recorded ))
  [ "$used" -ge "$BUDGET_MIN" ]
}

run_job() {
  local sha=$1 job=$2 dir out begin ec_test ec_vet
  dir=$(mktemp -d) out=$(mktemp -d)
  git worktree add -q --detach "$dir" "$job" || return 1
  begin=$(date +%s)
  log "job $sha: go vet + go test (4 CPU)"
  ( cd "$dir" && go version && go vet ./... ) >"$out/vet.log" 2>&1; ec_vet=$?
  ( cd "$dir" && go test -p 4 -count=1 ./... ) >"$out/test.log" 2>&1; ec_test=$?
  local finished tree
  finished=$(date +%s)
  tree=$(git rev-parse "$job^{tree}")
  cat >"$out/summary.json" <<JSON
{"schema":"mcpshka.gigaide-results/v1","source_sha":"$sha","snapshot":"$(git rev-parse "$job")","tree":"$tree","go":"$(go env GOVERSION)","exit_codes":{"vet":$ec_vet,"test":$ec_test},"seconds":$((finished - begin)),"finished_at":"$(date -u +%FT%TZ)","trust":"UNTRUSTED"}
JSON
  tail -n 200 "$out/test.log" >"$out/test-tail.log"
  tail -n 60 "$out/vet.log" >"$out/vet-tail.log"
  local entries="" f
  for f in summary.json test-tail.log vet-tail.log; do
    entries+=$(printf '100644 blob %s\t%s' "$(git hash-object -w "$out/$f")" "$f")$'\n'
  done
  local commit
  commit=$(git commit-tree "$(printf '%s' "$entries" | git mktree)" -m "gigaide result $sha vet=$ec_vet test=$ec_test")
  git push -q origin "$commit:refs/heads/gigaide-results/$sha" && log "job $sha: vet=$ec_vet test=$ec_test pushed"
  git worktree remove --force "$dir"; rm -rf "$out"
}

fetch
if over_budget; then
  log "monthly budget reached ($(used_minutes) of $BUDGET_MIN min): not starting. Close the workspace."
  exit 0
fi
log "started; budget $(used_minutes)/$BUDGET_MIN min used this month; idle exit after $IDLE_MIN min"
trap 'record; log "stopped"' EXIT
while true; do
  fetch
  for ref in $(git for-each-ref --format='%(refname)' refs/remotes/origin/gigaide-jobs/); do
    sha=${ref##*/}
    [[ $sha =~ ^[0-9a-f]{40}$ ]] || continue
    git rev-parse -q --verify "refs/remotes/origin/gigaide-results/$sha" >/dev/null && continue
    over_budget && { log "monthly budget reached during the queue"; exit 0; }
    run_job "$sha" "$ref"
    record || exit 1
    last_work=$(date +%s)
  done
  if [ $(( $(date +%s) - last_work )) -ge $(( IDLE_MIN * 60 )) ]; then
    log "idle $IDLE_MIN min: exiting. Close the browser tab so the workspace scales to zero."
    exit 0
  fi
  # Keep the ledger current even if the workspace is killed.
  [ $(( ( $(date +%s) - started ) / 60 - recorded )) -ge 10 ] && record
  over_budget && { log "monthly budget reached"; exit 0; }
  sleep 30
done
