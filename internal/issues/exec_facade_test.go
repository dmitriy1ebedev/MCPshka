package issues

import (
	"testing"
	"time"
)

func TestExecRepeatsBecomeFacadeFindings(t *testing.T) {
	now := time.Now()
	if execKey("/usr/bin/git", []string{"-C", "/root/repos/x", "blame", "--porcelain"}) != "git blame" || execKey("mcpshka", []string{"tool-invoke"}) != "" {
		t.Fatal("key")
	}
	for i := 0; i < execFacadeCalls; i++ {
		NoteExec("/usr/bin/git", []string{"-C", "/r", "blame"}, now.Add(time.Duration(i)*time.Minute))
	}
	NoteExec("/usr/bin/git", []string{"bisect"}, now)
	NoteExec("/usr/bin/df", nil, now.Add(-2*execFacadeWindow))
	f := ExecFacadeFindings(now.Add(time.Hour))
	if len(f) != 1 || f[0].Subject != "git blame" || f[0].Priority != "P2" {
		t.Fatalf("%+v", f)
	}
	if f := ExecFacadeFindings(now.Add(execFacadeWindow + time.Hour)); len(f) != 0 {
		t.Fatalf("a day later: %+v", f)
	}
}

// MCP-208/222 counted «nice 15»; MCP-191/205/207 filed go build/vet and gofmt,
// which already have native routes.
func TestExecKeyUnwrapsAndSkipsRouted(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		args []string
		want string
	}{
		{"nice", []string{"-n", "15", "du", "-sh"}, "du"},
		{"/usr/bin/nice", []string{"-n", "19", "ionice", "-c3", "rsync", "-a"}, "rsync"},
		{"timeout", []string{"30", "journalctl", "-u", "x"}, "journalctl x"},
		{"nice", []string{"-n", "15", "go", "build", "./..."}, ""},
		{"go", []string{"vet", "./..."}, ""},
		{"gofmt", []string{"-l", "."}, ""},
		{"nice", []string{"-n", "15"}, "nice"},
	} {
		if got := execKey(c.cmd, c.args); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.cmd, c.args, got, c.want)
		}
	}
}
