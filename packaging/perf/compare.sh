#!/bin/bash
# MCP-052 performance gate (CI runners only, never H96): run the same
# benchmarks on the base and candidate commits on one machine and fail when
# any is more than MAX_REGRESS percent slower or allocates more per op.
# usage: compare.sh BASE_REF [MAX_REGRESS]
set -euo pipefail
base_ref=${1:?base ref}; max=${2:-15}
pkgs="./internal/toolstats ./internal/roadmapnext ./internal/perfbudget ./internal/mcptransport"
work=$(mktemp -d); trap 'git worktree remove --force "$work/base" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
git worktree add -q --detach "$work/base" "$base_ref"
bench() { (cd "$1" && go test -run '^$' -bench . -benchmem -count 5 $pkgs 2>/dev/null) || true; }
bench "$work/base" > "$work/base.txt"
bench . > "$work/cand.txt"
go run ./cmd/mcpshka perf-compare "$work/base.txt" "$work/cand.txt" "$max"
