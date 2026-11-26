package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestChatArchiveDistillation: an export in memory-inbox becomes pending
// chunks; a distilled record marks its chunk, a skip closes one, coverage
// counts both, and a rescan adds nothing twice (0.11.3 item 8).
func TestChatArchiveDistillation(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	inbox := filepath.Join(filepath.Dir(s.file), inboxDir)
	if err := os.MkdirAll(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	claude := `[{"name":"Свет в доме","created_at":"2026-11-01T10:00:00Z","chat_messages":[{"sender":"human","text":"как включить свет"},{"sender":"assistant","text":"через ha_service_call"}]},
{"name":"Пустая","created_at":"2026-11-02T10:00:00Z","chat_messages":[{"sender":"human","text":"привет"}]}]`
	if err := os.WriteFile(filepath.Join(inbox, "claude.json"), []byte(claude), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := s.InboxScan(ctx)
	if err != nil || rep.Files != 1 || rep.Pending != 2 {
		t.Fatalf("scan: %+v %v", rep, err)
	}
	next, err := RecallWith(ctx, func() (*Store, error) { return s, nil }, Query{Inbox: true, Limit: 5})
	if err != nil || len(next.Hits) != 2 || next.Hits[0].Title != "Свет в доме" {
		t.Fatalf("next: %+v %v", next, err)
	}
	first, second := next.Hits[0].Source, next.Hits[1].Source
	if _, err := WriteWith(ctx, func() (*Store, error) { return s, nil }, WriteInput{Record: Record{Kind: "decision", Title: "Свет через HA", Body: "ha_service_call", Source: first, Questions: []string{"как включить свет"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteWith(ctx, func() (*Store, error) { return s, nil }, WriteInput{InboxSkip: []string{second}}); err != nil {
		t.Fatal(err)
	}
	rep, err = s.InboxScan(ctx)
	if err != nil || rep.Files != 0 || rep.Pending != 0 || rep.Distilled != 1 || rep.Skipped != 1 || rep.Records != 1 {
		t.Fatalf("coverage: %+v %v", rep, err)
	}
	if line := s.inboxLine(ctx); line != "" {
		t.Fatalf("nothing pending, brief must stay short: %q", line)
	}
}
