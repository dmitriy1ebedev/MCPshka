package executor

import (
	"os/exec"
	"strings"
	"testing"
)

// BUG-20261026-134: a chatty suite must finish with its own exit code, not
// SIGPIPE, and its kept output must fit the 8 KiB result with a notice.
func TestSuiteOutputNeverBreaksChildPipe(t *testing.T) {
	var out suiteOutput
	cmd := exec.Command("/bin/sh", "-c", `echo FIRST; i=0; while [ $i -lt 4000 ]; do echo "line $i of a long suite"; i=$((i+1)); done; echo LAST; exit 3`)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if code := cmd.ProcessState.ExitCode(); code != 3 {
		t.Fatalf("exit=%d err=%v, want the suite's own 3", code, err)
	}
	text := out.String()
	if !strings.HasPrefix(text, "FIRST\n") || !strings.Contains(text, "LAST\n") || !strings.Contains(text, "bytes omitted") {
		t.Fatalf("head/tail lost: %q…%q", text[:40], text[len(text)-40:])
	}
	if len(text)+256 > 8192 {
		t.Fatalf("kept %d bytes, no room for the timeout notice", len(text))
	}
}

func TestSuiteOutputShortIsVerbatim(t *testing.T) {
	var out suiteOutput
	out.Write([]byte("ok\n"))
	out.Write([]byte("PASS\n"))
	if out.String() != "ok\nPASS\n" {
		t.Fatalf("%q", out.String())
	}
}
