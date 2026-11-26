package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncSessionsIncremental(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	dir := t.TempDir()
	cc := filepath.Join(dir, "-root", "a.jsonl")
	if err := os.MkdirAll(filepath.Dir(cc), 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","timestamp":"2026-11-23T10:00:00Z","message":{"role":"user","content":"собери релиз, токен ghp_abcdefghijklmnopqrstuvwxyz0123456789"}}`,
		`{"type":"assistant","timestamp":"2026-11-23T10:00:05Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"x"}]}}`,
		`{"type":"user","timestamp":"2026-11-23T10:00:06Z","message":{"role":"user","content":[{"type":"tool_result","content":"secret output"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-23T10:00:07Z","message":{"role":"assistant","content":[{"type":"text","text":"промежуточно"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-23T10:00:09Z","message":{"role":"assistant","content":[{"type":"text","text":"Выпуск 0.17.5 установлен"}]}}`,
	}
	if err := os.WriteFile(cc, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(dir, "2026", "b.jsonl")
	_ = os.MkdirAll(filepath.Dir(codex), 0o700)
	_ = os.WriteFile(codex, []byte(`{"timestamp":"2026-11-23T11:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd</environment_context>"}]}}
{"timestamp":"2026-11-23T11:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"почини lane"}]}}
{"timestamp":"2026-11-23T11:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"lane починен"}]}}
`), 0o600)
	rep, err := s.SyncSessions(ctx, []string{dir})
	if err != nil || rep.Files != 2 || rep.Chunks != 2 || rep.State != "completed" || rep.Processed != 2 {
		t.Fatalf("first sync %+v %v", rep, err)
	}
	hits, _ := s.InboxNext(ctx, 5)
	all := ""
	for _, h := range hits {
		all += h.Body
	}
	for _, want := range []string{"owner: собери релиз", "assistant: Выпуск 0.17.5 установлен", "owner: почини lane", "assistant: lane починен"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in %q", want, all)
		}
	}
	for _, bad := range []string{"ghp_abcdefghijklmnopqrstuvwxyz0123456789", "secret output", "промежуточно", "environment_context"} {
		if strings.Contains(all, bad) {
			t.Fatalf("leaked %q", bad)
		}
	}
	if again, _ := s.SyncSessions(ctx, []string{dir}); again.Files != 0 || again.Chunks != 0 || again.State != "idle" {
		t.Fatalf("rerun not idempotent: %+v", again)
	}
	f, _ := os.OpenFile(cc, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"type":"user","timestamp":"2026-11-23T12:00:00Z","message":{"role":"user","content":"теперь память"}}` + "\n" + `{"type":"user","timestamp":"2026-11-23T12:00:0`)
	f.Close()
	next, err := s.SyncSessions(ctx, []string{dir})
	if err != nil || next.Files != 1 || next.Chunks != 1 || next.Inbox.Pending != 3 {
		t.Fatalf("append sync %+v %v", next, err)
	}
}

func TestSyncSessionsPartialLineHasNoEffect(t *testing.T) {
	s := store(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pending.jsonl"), []byte("{\"type\":\"user\""), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := s.SyncSessions(context.Background(), []string{dir})
	if err != nil || report.State != "no_effect" || report.Effect || report.NextSafeAction == "" {
		t.Fatal(report, err)
	}
}
