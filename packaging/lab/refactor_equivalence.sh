#!/bin/bash
# MCP-068: prove a structural refactoring changed no exported API and lost no
# test. Usage: refactor_equivalence.sh BASE_REF HEAD_REF  (run in CI, not on H96)
set -euo pipefail
base=${1:?base ref}; head=${2:?head ref}
work=$(mktemp -d); trap 'git worktree remove --force "$work/base" 2>/dev/null; git worktree remove --force "$work/head" 2>/dev/null; rm -rf "$work"' EXIT
git worktree add -q --detach "$work/base" "$base"
git worktree add -q --detach "$work/head" "$head"
go run ./cmd/mcpshka api-surface "$work/base" > "$work/base.txt"
go run ./cmd/mcpshka api-surface "$work/head" > "$work/head.txt"
if ! diff -u "$work/base.txt" "$work/head.txt"; then
  echo "REFACTOR_NOT_EQUIVALENT: exported API or test set changed" >&2
  exit 1
fi
echo "refactor equivalent: $(wc -l < "$work/head.txt") API/test lines unchanged"
