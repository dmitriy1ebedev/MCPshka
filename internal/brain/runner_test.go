package brain

import (
	"context"
	"testing"
	"time"

	"mcpshka/internal/llmgateway"
)

func TestRunnerBackgroundResumeAndIdempotentStart(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	llm := func(string) LLM {
		return func(ctx context.Context, msgs []llmgateway.Message, _ []llmgateway.Function) (llmgateway.Turn, error) {
			if len(msgs) == 2 {
				return llmgateway.Turn{Call: &llmgateway.FunctionCall{Name: "memory_recall", Arguments: map[string]any{"text": "x"}}, Model: "m"}, nil
			}
			select {
			case <-release:
				return llmgateway.Turn{Text: "готово", Model: "m"}, nil
			case <-ctx.Done():
				return llmgateway.Turn{}, ctx.Err()
			}
		}
	}
	call := func(context.Context, string, map[string]any) (string, error) { return "{}", nil }
	r := &Runner{Dir: dir, LLM: llm, Call: call}
	s, err := r.Start("k1", "задача", "", 5)
	if err != nil || s.State != "running" {
		t.Fatalf("%+v %v", s, err)
	}
	if again, _ := r.Start("k1", "другая", "", 5); again.ID != s.ID || again.Task != "задача" {
		t.Fatalf("same key started a second run: %+v", again)
	}
	waitFor(t, func() bool { rec, _ := r.Load(s.ID); return rec != nil && len(rec.Steps) == 1 })
	// Simulate a restart: a fresh runner resumes the saved run.
	r.mu.Lock()
	for _, c := range r.cancels {
		c()
	}
	r.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	r2 := &Runner{Dir: dir, LLM: llm, Call: call}
	if n := r2.Resume(); n != 1 {
		t.Fatalf("resumed %d", n)
	}
	close(release)
	waitFor(t, func() bool { rec, _ := r2.Load(s.ID); return rec != nil && rec.State == "done" })
	if rec, _ := r2.Load(s.ID); rec.Answer != "готово" || len(rec.Steps) != 1 {
		t.Fatalf("%+v", rec.Summary())
	}
	if c, _ := r2.Cancel(s.ID); c.State != "done" {
		t.Fatalf("cancel changed a finished run: %+v", c)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout")
}
