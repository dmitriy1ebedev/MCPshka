package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMeasureNudgesReadsOnlyOwnerTurns(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "chat.jsonl")
	raw := `{"type":"user","timestamp":"2026-11-26T12:00:00.500Z","message":{"role":"user","content":"Продолжай, пожалуйста"}}
{"type":"assistant","timestamp":"2026-11-26T12:00:01Z","message":{"role":"assistant","content":"почему остановился"}}
{"type":"user","timestamp":"2026-11-26T12:00:02Z","message":{"role":"user","content":"«дальше» — пример слова"}}
{"type":"user","timestamp":"2026-11-26T12:00:03Z","message":{"role":"user","content":"Не останавливайся!"}}
`
	if err := os.WriteFile(f, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncSessions(ctx, []string{filepath.Dir(f)}); err != nil {
		t.Fatal(err)
	}
	d := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	measure := func(project string) Nudges {
		t.Helper()
		got, err := MeasureNudges(ctx, filepath.Dir(s.file), project, d, d.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := measure(""); got.Nudges != 2 || got.Chunks != 1 {
		t.Fatalf("%+v", got)
	}
	// MCP-286: no chat of the project — all chats count, marked Unscoped.
	if got := measure("/root/repos/demo"); !got.Unscoped || got.Nudges != 2 {
		t.Fatalf("unattributed: %+v", got)
	}
	rows, err := s.exec(ctx, "SELECT id FROM mem_inbox;")
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	if _, _, err := s.Write(ctx, Record{Kind: "note", Title: "Chat import", Body: "A project decision.", Source: str(rows[0], "id"), Project: "demo"}); err != nil {
		t.Fatal(err)
	}
	if got := measure("/root/repos/demo"); got.Nudges != 2 || got.Unscoped {
		t.Fatalf("attributed: %+v", got)
	}
	// A chat that names the project counts as the project's.
	if got := measure("/root/repos/Pachuli"); !got.Unscoped {
		t.Fatalf("other project: %+v", got)
	}
}
