package issues

import (
	"strings"
	"testing"
	"time"
)

func TestExecKeyNamesTheRealCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		args    []string
		want    string
	}{
		{"plain command", "/usr/bin/df", nil, "df"},
		{"first non-flag argument", "git", []string{"blame", "--porcelain"}, "git blame"},
		{"routed git log (MCP-266)", "git", []string{"log", "--oneline"}, ""},
		{"routed git rev-parse (MCP-261)", "git", []string{"rev-parse", "HEAD"}, ""},
		{"routed git fetch (MCP-268)", "git", []string{"fetch", "origin"}, ""},
		{"git add is a counted facade", "git", []string{"add", "README.md"}, "git add"},
		{"dash C and paths are noise", "/usr/bin/git", []string{"-C", "/root/repos/x", "diff"}, ""},
		{"wrapper with flag and number", "nice", []string{"-n", "15", "make", "all"}, "make all"},
		{"env assignments are skipped", "env", []string{"FOO=bar", "make", "all"}, "make all"},
		{"wrapper alone", "timeout", []string{"30"}, "timeout"},
		{"routed go build", "nice", []string{"-n", "19", "go", "build"}, ""},
		{"routed go test", "go", []string{"test", "./..."}, ""},
		{"routed go vet", "go", []string{"vet", "./..."}, ""},
		{"routed gofmt with a dot argument", "gofmt", []string{"-l", "."}, ""},
		{"routed git status", "git", []string{"status", "--short"}, ""},
		{"mcpshka compat bridge", "mcpshka", []string{"guide", "rules"}, ""},
		{"overlong argument is not part of the key", "echo", []string{strings.Repeat("a", 33)}, "echo"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := execKey(tc.command, tc.args); got != tc.want {
				t.Fatalf("execKey(%q, %v) = %q, want %q", tc.command, tc.args, got, tc.want)
			}
		})
	}
}

func execKeyTestReset() {
	execRepeats.Lock()
	execRepeats.at = map[string][]time.Time{}
	execRepeats.Unlock()
}

func TestExecFacadeFindingsSkipRoutedCommandsAndSortBySubject(t *testing.T) {
	execKeyTestReset()
	defer execKeyTestReset()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < execFacadeCalls; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		NoteExec("/usr/bin/zstat", nil, at)
		NoteExec("git", []string{"add", "x"}, at)
		NoteExec("go", []string{"test", "./..."}, at)
		NoteExec("mcpshka", []string{"guide", "rules"}, at)
	}
	for i := 0; i < execFacadeCalls-1; i++ {
		NoteExec("jq", []string{".a"}, now.Add(time.Duration(i)*time.Minute))
	}
	got := ExecFacadeFindings(now.Add(time.Hour))
	if len(got) != 2 || got[0].Subject != "git add" || got[1].Subject != "zstat" {
		t.Fatalf("only unrouted commands at the threshold, sorted by subject: %+v", got)
	}
	for _, f := range got {
		if f.Kind != "exec_facade" || f.Priority != "P2" || !strings.Contains(f.Evidence, "запущен 5 раз за сутки") {
			t.Fatalf("finding shape: %+v", f)
		}
	}
}

func TestExecFacadeWindowDropsOldCallsPerCommand(t *testing.T) {
	execKeyTestReset()
	defer execKeyTestReset()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < execFacadeCalls-1; i++ {
		NoteExec("tree", nil, now.Add(-2*execFacadeWindow))
	}
	NoteExec("tree", nil, now)
	if got := ExecFacadeFindings(now.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("calls older than the window must not count: %+v", got)
	}
	for i := 0; i < execFacadeCalls-1; i++ {
		NoteExec("tree", nil, now.Add(time.Duration(i+1)*time.Minute))
	}
	got := ExecFacadeFindings(now.Add(time.Hour))
	if len(got) != 1 || got[0].Subject != "tree" {
		t.Fatalf("five calls inside the window must be reported: %+v", got)
	}
}
