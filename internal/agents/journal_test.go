package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/heavyqueue"
)

// MCP-060: a task records why it ran, HEAD before/after, branch and the test
// jobs it reported; a new commit is committed_not_released, never DONE.
func TestTaskJournalLinksResumeAndTestEvidence(t *testing.T) {
	previous, oldHead := taskHeavyQueue, gitHead
	t.Cleanup(func() { taskHeavyQueue, gitHead, taskDir = previous, oldHead, TaskDir })
	taskHeavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	taskDir = t.TempDir()
	heads := []string{"aaaa", "bbbb"}
	gitHead = func(ctx context.Context, path string) (string, string) {
		h := heads[0]
		if len(heads) > 1 {
			heads = heads[1:]
		}
		return h, "release/0.3.0"
	}
	id := "00000000000000aa"
	if err := os.WriteFile(filepath.Join(taskDir, id+".prompt"), []byte("## Закрыть MCP-060\nдетали"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, agent, dir, prompt, report string) (string, error) {
		return "project_test job mvzlptmu-l PASS; job_id: mvzlptmu-l", nil
	}
	got, err := RunTask(context.Background(), id, Codex, "/root/repos/diag", run)
	if err != nil || got.Journal == nil {
		t.Fatalf("%+v %v", got, err)
	}
	j := got.Journal
	if j.Purpose != "Закрыть MCP-060" || j.HeadBefore != "aaaa" || j.HeadAfter != "bbbb" || j.Branch != "release/0.3.0" ||
		len(j.TestJobs) != 1 || j.TestJobs[0] != "mvzlptmu-l" || j.Disposition != "committed_not_released" {
		t.Fatalf("journal: %+v", j)
	}
	if read, _ := ReadTask(id); read.Journal == nil || read.Journal.Disposition != "committed_not_released" {
		t.Fatal("journal not persisted")
	}
}
