package issues

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A command an AI keeps starting by hand through exec_start is a missing
// native tool (ROADMAP S5). The sentinel files one card per command once it
// was started execFacadeCalls times within a day, two passes in a row.
const (
	execFacadeCalls  = 5
	execFacadeWindow = 24 * time.Hour
)

var execRepeats = struct {
	sync.Mutex
	at map[string][]time.Time
}{at: map[string][]time.Time{}}

// execWrappers only change how a command runs: `nice -n 15 go build` is
// `go build` (MCP-208, MCP-222 counted «nice 15»/«nice 19»).
var execWrappers = map[string]bool{"nice": true, "ionice": true, "timeout": true, "env": true, "sudo": true, "chrt": true, "taskset": true, "stdbuf": true}

// execRouted already have a native tool; the facade is the AI's routing
// mistake, not a missing tool, so no card (MCP-191, MCP-205, MCP-207): the
// guide (rules п.10) and exec_start's refusal name the route.
var execRouted = map[string]string{"go build": "project_build", "go test": "project_test", "go vet": "project_test", "gofmt": "project_commit", "git status": "git_status", "git diff": "git_diff", "git log": "git_log", "git rev-parse": "git_log", "git show": "git_log", "git fetch": "git_provider_query"}

// execKey is the command's base name and its first non-flag argument:
// `git status` and `git log` are different facades, `-C dir` is noise.
func execKey(command string, args []string) string {
	key := filepath.Base(command)
	for execWrappers[key] {
		i := 0
		for i < len(args) && (strings.HasPrefix(args[i], "-") || strings.ContainsAny(args[i], "=") || isNumber(args[i])) {
			i++
		}
		if i == len(args) {
			return key
		}
		key, args = filepath.Base(args[i]), args[i+1:]
	}
	if key == "mcpshka" { // compat bridge to tools, not a missing one
		return ""
	}
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") && !strings.ContainsAny(a, "/=") && len(a) <= 32 {
			key += " " + a
			break
		}
	}
	if _, ok := execRouted[key]; ok {
		return ""
	}
	if _, ok := execRouted[strings.Fields(key)[0]]; ok {
		return ""
	}
	return key
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// NoteExec counts one exec_start (the MCP handler calls it).
func NoteExec(command string, args []string, now time.Time) {
	key := execKey(command, args)
	if key == "" {
		return
	}
	execRepeats.Lock()
	defer execRepeats.Unlock()
	execRepeats.at[key] = append(trimExec(execRepeats.at[key], now), now)
}

func trimExec(ts []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > execFacadeWindow {
		i++
	}
	return ts[i:]
}

// ExecFacadeFindings lists the commands over the threshold, worst first.
func ExecFacadeFindings(now time.Time) []Finding {
	execRepeats.Lock()
	defer execRepeats.Unlock()
	var out []Finding
	for key, ts := range execRepeats.at {
		if ts = trimExec(ts, now); len(ts) == 0 {
			delete(execRepeats.at, key)
			continue
		} else if execRepeats.at[key] = ts; len(ts) < execFacadeCalls {
			continue
		}
		out = append(out, Finding{Severity: "critical", Priority: "P2", Kind: "exec_facade", Subject: key,
			Evidence: fmt.Sprintf("exec_start «%s» запущен %d раз за сутки", key, len(ts)),
			Action:   "дать нативный инструмент (или расширить существующий) вместо ручного exec_start; tool_usage покажет цепочки"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}
