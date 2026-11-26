package jobs

import (
	"context"
	"strings"
	"testing"

	"mcpshka/internal/runoutcome"
)

func TestLLMJobSurvivesClientAndCancels(t *testing.T) {
	m, _ := testManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	j, e := m.StartLLM(ctx, func(ctx context.Context) (any, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() })
	if e != nil {
		t.Fatal(e)
	}
	<-entered
	cancel()
	s, e := m.Get(j.ID)
	if e != nil || s.State != Running {
		t.Fatal(s, e)
	}
	if _, e = m.StartLLM(context.Background(), func(context.Context) (any, error) { return nil, nil }); e == nil {
		t.Fatal("unbounded parallel LLM jobs")
	}
	if _, e = m.Cancel(j.ID); e != nil {
		t.Fatal(e)
	}
	if s = waitForTerminal(t, m, j.ID); s.State != Cancelled {
		t.Fatal(s)
	}
	j, e = m.StartLLM(context.Background(), func(context.Context) (any, error) { return map[string]string{"text": "answer"}, nil })
	if e != nil {
		t.Fatal(e)
	}
	if s = waitForTerminal(t, m, j.ID); s.State != Done {
		t.Fatal(s)
	}
	out, e := m.Read(j.ID, "stdout", 0, 1024)
	if e != nil || !strings.Contains(out.Text, "answer") {
		t.Fatal(out, e)
	}
}

func TestLLMRestartAllowsFreshReadOnlyRequest(t *testing.T) {
	m, work := testManager(t)
	s, e := m.StoreReceipt("mcpshka-llm", nil, work, "")
	if e != nil {
		t.Fatal(e)
	}
	s.State = Running
	if e = m.persist(s); e != nil {
		t.Fatal(e)
	}
	restored, e := New(m.root, m.policy)
	if e != nil {
		t.Fatal(e)
	}
	j, e := restored.Get(s.ID)
	if e != nil || j.State != Failed || !j.ResourcesReleased || j.FinishedAt.IsZero() {
		t.Fatal(j, e)
	}
	j, e = restored.StartLLM(context.Background(), func(context.Context) (any, error) { return "fresh", nil })
	if e != nil {
		t.Fatal(e)
	}
	if j = waitForTerminal(t, restored, j.ID); j.State != Done {
		t.Fatal(j)
	}
}

func TestLLMJobOutputIsBounded(t *testing.T) {
	m, _ := testManager(t)
	j, e := m.StartLLM(context.Background(), func(context.Context) (any, error) { return strings.Repeat("x", llmOutputLimit+1), nil })
	if e != nil {
		t.Fatal(e)
	}
	if j = waitForTerminal(t, m, j.ID); j.State != Failed {
		t.Fatal(j)
	}
	out, e := m.Read(j.ID, "stdout", 0, 1024)
	if e != nil || len(out.Text) > 1024 || !strings.Contains(out.Text, "error") {
		t.Fatal(out, e)
	}
}

type testScheduledOutcome struct{ runoutcome.Result }

func (r testScheduledOutcome) Outcome() runoutcome.Result { return r.Result }

func TestNativeScheduledJobKeepsNoEffectReceipt(t *testing.T) {
	m, _ := testManager(t)
	j, err := m.StartFixes(context.Background(), func(context.Context) (any, error) {
		return testScheduledOutcome{runoutcome.Resolve(0, true, "cursor_blocked", "inspect saved offset")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	j = waitForTerminal(t, m, j.ID)
	if j.State != NoEffect || j.Error != "cursor_blocked" || j.ExitCode == nil || *j.ExitCode == 0 {
		t.Fatal(j)
	}
	out, err := m.Read(j.ID, "stdout", 0, 1024)
	if err != nil || !strings.Contains(out.Text, "inspect saved offset") {
		t.Fatal(out, err)
	}
}
