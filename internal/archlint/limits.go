package archlint

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// maxFileLines is the size of one readable concern; past it, split by
// concern (see internal/jobs) or move the module out (see internal/ownerweb).
const maxFileLines = 700

// oversized are the files over the limit; each may only shrink. Empty since
// 0.18.0: every file was split under maxFileLines — keep it that way.
var oversized = map[string]int{}

// packageLines caps the non-test Go lines of the packages that grew into
// monoliths. New tool families go to their own package, not here.
var packageLines = map[string]int{
	"internal/mcpserver":  8751, // 0.19.3: failure envelope → tooldef/failure, tool timing → tools/autonomy, issue snapshot → issuesnap; 0.19.0: approved_task → tools/approvedtask, phase gate → phasegate, project_context → projectctx, failure classes → failure, exec_start compatibility table; -30 (0.18.0): memory tools moved to internal/tools/memorytools; -22 (0.17.4): job artifact GC tools moved to internal/tools/jobgc
	"internal/rootbroker": 9475, // S5.GO.4: DIAG traffic observer/validation → rootbroker/diagtraffic; 0.19.0: systemd-run validation → transientunit, docker terminal, self-test, Docker smoke, agentlogin, wifiscan, rootledgerreap, diagobserver, diagcontract, hostcleanup, DIAG install → own packages; +72 MCP-246 session-covered approved_task; -2 (0.18.0): self-heal result type moved to internal/selfheal while S5.LEASE was wired in; -48 (0.17.4): Venta local Git identity moved to internal/ventagit; +6 (0.12.9): agent-task WORKTREE REPO form, consent for REPO (the broker cannot see /root/repos; the runner re-checks); +8 (P12): fixed diagnostic fields and class wiring in Codex session receipts; logic in internal/codexcoord.  +5 (0.12.8): agent consent checks a worktree as its repository (injectable lookup for the test); +3 (0.12.7): agent-task-cancel only stops units (-1), agent tasks in repository worktrees (+4, helper in internal/repopath); +2 (0.12.6): docker inspect over the Engine API (internal/dockerapi); +4 (0.12.5): systemctl show over D-Bus and docker ps over the Engine API (the clients live in internal/unitbus, internal/dockerapi); +6 (0.12.1): a silent exit-1 approved task explains itself (MCP-177); +8 (0.11.8): Face ID window check for project-release
	"internal/executor":   9700, // 0.19.2: recipe package artifacts → internal/pipelineartifact (-171; affected gate +38, shard/suite checks → distributedci); 0.19.0: systemd fence + Identity → unitfence, runtime binds → sandboxbind; +20 cleanup keeps older-contract manifests; +16 (0.18.0): release_cleanup.go split (794 → 660, imports of release_cleanup_item.go); +6 (0.17.4): Venta CI evidence skips the local tree-ancestry check its authenticated provider already proved (MCP-269); -46 (0.13.3): go.sum parsing and cache naming moved to internal/gomodcache; -13 (0.12.5): fence evidence through unitbus.Bounded
}

// FileLimit returns the current cap for a repository-relative Go source path.
func FileLimit(name string) int {
	if limit, ok := oversized[name]; ok {
		return limit
	}
	return maxFileLines
}

// Growth reports the same upper limits enforced by the architecture tests.
func Growth(counts map[string]int) []string {
	totals := map[string]int{}
	var violations []string
	for name, count := range counts {
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if limit := FileLimit(name); count > limit {
			violations = append(violations, fmt.Sprintf("%s has %d lines, cap %d", name, count, limit))
		}
		totals[path.Dir(name)] += count
	}
	for name, limit := range packageLines {
		if count := totals[name]; count > limit {
			violations = append(violations, fmt.Sprintf("%s has %d lines, cap %d", name, count, limit))
		}
	}
	sort.Strings(violations)
	return violations
}
