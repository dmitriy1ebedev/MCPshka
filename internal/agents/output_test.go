package agents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"mcpshka/internal/heavyqueue"
)

func TestAgentOutputBoundedWithoutBreakingChildOrLosingOutcome(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.md")
	cmd := exec.Command("/bin/sh", "-c", "printf 'FIRST\\n'; i=0; while [ $i -lt 8000 ]; do printf 'long process output line %s\\n' $i; i=$((i+1)); done; printf 'LAST ✅\\n'; exit 3")
	tail, err := runCommand(context.Background(), cmd, Claude, report)
	if cmd.ProcessState.ExitCode() != 3 || err == nil {
		t.Fatal("child exit changed", cmd.ProcessState, err)
	}
	if len(tail) > 64<<10 || !strings.HasPrefix(tail, "FIRST\n") || !strings.Contains(tail, "LAST ✅") || !strings.Contains(tail, "bytes omitted") {
		t.Fatal("bounded head/tail or truncation proof lost", len(tail))
	}
	data, readErr := os.ReadFile(report)
	if readErr != nil || len(data) > 64<<10 || !utf8.Valid(data) {
		t.Fatal("unbounded or invalid report", len(data), readErr)
	}
}

func TestCodexReportCannotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	secret, report := filepath.Join(dir, "secret"), filepath.Join(dir, "report")
	if err := os.WriteFile(secret, []byte("private-target-never-read"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, report); err != nil {
		t.Fatal(err)
	}
	tail, err := runCommand(context.Background(), exec.Command("/bin/sh", "-c", "printf stdout-only"), Codex, report)
	if err != nil || strings.Contains(tail, "private-target") || !strings.Contains(tail, "stdout-only") {
		t.Fatal("followed report symlink", err)
	}
}

func TestAgentTaskTailAlwaysValidUTF8(t *testing.T) {
	priorQueue, priorDir := taskHeavyQueue, taskDir
	t.Cleanup(func() { taskHeavyQueue, taskDir = priorQueue, priorDir })
	taskDir = t.TempDir()
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Ready: func() (bool, string) { return true, "" }}
	const id = "1234567890abcdef"
	if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := RunTask(context.Background(), id, Codex, t.TempDir(), func(context.Context, string, string, string, string) (string, error) {
		return strings.Repeat("✅", 4000) + "END", nil
	})
	if err != nil || len(task.Tail) > 8<<10 || !utf8.ValidString(task.Tail) || !strings.HasSuffix(task.Tail, "END") {
		t.Fatal("invalid task tail", len(task.Tail), err)
	}
}

func TestCapacityErrorDoesNotClaimNoPriorEffect(t *testing.T) {
	text := (&CapacityError{Agent: Codex}).Error()
	if strings.Contains(text, "no work") || !strings.Contains(text, "reconcile") {
		t.Fatal("provider text fabricated no-effect proof", text)
	}
}

func TestAgentReportReadIsBoundedAndWritesRejectLinks(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "large")
	data := strings.Repeat("x", 2<<20) + "FINAL✅"
	if err := os.WriteFile(report, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readReportTail(report)
	if err != nil || len(got) > 64<<10 || !strings.HasSuffix(string(got), "FINAL✅") {
		t.Fatal("unbounded final report", len(got), err)
	}
	target := filepath.Join(dir, "target")
	original := []byte("preserved user data")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, hard := range []bool{false, true} {
		link := filepath.Join(dir, map[bool]string{false: "symlink", true: "hardlink"}[hard])
		var err error
		if hard {
			err = os.Link(target, link)
		} else {
			err = os.Symlink(target, link)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := writeReport(link, []byte("overwrite")); err == nil {
			t.Fatal("report link accepted", hard)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != string(original) {
			t.Fatal("report write clobbered linked file", err)
		}
	}
}

func TestBorrowedLeaseCredentialRemovedFromOutputAndReport(t *testing.T) {
	for _, agent := range []string{Codex, Claude} {
		report := filepath.Join(t.TempDir(), "report.md")
		if err := os.WriteFile(report, []byte("borrowed-private-token report"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx := context.WithValue(context.Background(), taskSecretKey{}, "borrowed-private-token")
		tail, err := runCommand(ctx, exec.Command("/bin/sh", "-c", "printf 'borrowed-private-token stdout'"), agent, report)
		if err != nil || strings.Contains(tail, "borrowed-private-token") {
			t.Fatal("credential exposed in output", agent, err)
		}
		raw, err := os.ReadFile(report)
		if err != nil || strings.Contains(string(raw), "borrowed-private-token") {
			t.Fatal("credential exposed in report", agent, err)
		}
	}
}

func TestTaskCheckpointDoesNotFollowPredictableTemporaryLink(t *testing.T) {
	prior := taskDir
	t.Cleanup(func() { taskDir = prior })
	taskDir = t.TempDir()
	target := filepath.Join(taskDir, "preserved")
	if err := os.WriteFile(target, []byte("owner data"), 0600); err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef"
	if err := os.Symlink(target, filepath.Join(taskDir, id+".json.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(Task{ID: id, State: "running"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "owner data" {
		t.Fatal("checkpoint clobbered linked file", err)
	}
}
