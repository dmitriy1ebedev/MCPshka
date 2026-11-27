package knowledge_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/knowledge"
	"mcpshka/internal/searchindex"
)

// MCP-035: imported chats keep their own dates and search filters by date,
// project and phrase without publishing secrets.
func TestImportChatKeepsDatesAndSearchFiltersByDate(t *testing.T) {
	s, err := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.db"))
	if err != nil {
		t.Fatal(err)
	}
	gpt := `[{"title":"r20","create_time":1761955200.5,"mapping":{"a":{"message":{"create_time":1761958800,"author":{"role":"user"},"content":{"parts":["BUG lease stale token=sk-abcdefghijklmnopqrstuvwxyz123456"]}}}}}]`
	ids, err := s.ImportChat(strings.NewReader(gpt), "chatgpt", "artifact:gpt", "aaaaaa")
	if err != nil || len(ids) != 1 {
		t.Fatalf("%v %v", ids, err)
	}
	r, _ := s.Inspect(ids[0])
	if want := time.Unix(1761958800, 0).UTC(); !r.Timestamp.Equal(want) {
		t.Fatalf("message time lost: %v want %v", r.Timestamp, want)
	}
	claude := `[{"name":"old","created_at":"2026-10-01T09:00:00Z","chat_messages":[{"sender":"human","text":"BUG lease from october"}]}]`
	if _, err := s.ImportChat(strings.NewReader(claude), "claude", "artifact:cl", "bbbbbb"); err != nil {
		t.Fatal(err)
	}
	recs := []searchindex.Record{
		{ID: "a", Source: "s", Provenance: "p", Text: "BUG lease november", Timestamp: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)},
		{ID: "b", Source: "s", Provenance: "p", Text: "BUG lease october", Timestamp: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)},
	}
	src := staticSource(recs)
	hits, err := searchindex.Search(context.Background(), src, searchindex.Query{Text: "lease", Since: "2026-10-31", Limit: 10}, 10)
	if err != nil || len(hits) != 1 || hits[0].Record.ID != "a" {
		t.Fatalf("since: %+v %v", hits, err)
	}
	hits, _ = searchindex.Search(context.Background(), src, searchindex.Query{Text: "lease", Until: "2026-10-01", Limit: 10}, 10)
	if len(hits) != 1 || hits[0].Record.ID != "b" {
		t.Fatalf("until includes the whole day: %+v", hits)
	}
	if _, err := searchindex.Search(context.Background(), src, searchindex.Query{Text: "lease", Since: "yesterday", Limit: 10}, 10); err != searchindex.ErrInvalidTime {
		t.Fatalf("bad date accepted: %v", err)
	}
	if strings.Contains(r.Fields["content"].Value, "sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatal("secret published")
	}
}

type staticSource []searchindex.Record

func (s staticSource) Snapshot(context.Context, int) ([]searchindex.Record, error) {
	return append([]searchindex.Record(nil), s...), nil
}
