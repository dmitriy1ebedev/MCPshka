package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

func store(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecallRussianFormsAndRanking(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	for _, r := range []Record{
		{Kind: "decision", Project: "h96", Title: "OOM torrserver", Body: "Контейнер торрсервера падал по памяти из-за индекса rutor; отключили поиск rutor через API и сделали запуск по требованию через systemd socket.", Source: "chat 10.11"},
		{Kind: "lesson", Project: "mcpshka", Title: "Drive rate limit", Body: "Общий client_id rclone упирается в rateLimitExceeded; нужен свой OAuth-клиент и публикация приложения.", Source: "MCP-165"},
		{Kind: "note", Title: "Кулер", Body: "Кулер USB управляется автоматизацией Home Assistant по температуре процессора."},
	} {
		if _, _, err := s.Write(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	hits, err := s.Recall(ctx, Query{Text: "как чинили падения торрсервера по памяти"})
	if err != nil || len(hits) == 0 || hits[0].Title != "OOM torrserver" {
		t.Fatalf("torrserver not first: %+v %v", hits, err)
	}
	if d := time.Since(started); d > 2*time.Second {
		t.Fatalf("recall too slow: %v", d)
	}
	hits, err = s.Recall(ctx, Query{Text: "лимит запросов гугл драйв rclone"})
	if err != nil || len(hits) == 0 || hits[0].Source != "MCP-165" {
		t.Fatalf("drive lesson not first: %+v %v", hits, err)
	}
	if _, err = s.Recall(ctx, Query{Text: "!!"}); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestBiTemporalSupersede(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	day1 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return day1 }
	first, _, err := s.Write(ctx, Record{Kind: "fact", Project: "h96", Title: "порог кулера", Body: "кулер включается при 55 градусах"})
	if err != nil {
		t.Fatal(err)
	}
	if same, changed, _ := s.Write(ctx, Record{Kind: "fact", Project: "h96", Title: "Порог  кулера", Body: "кулер включается при 55 градусах"}); changed || same.ID != first.ID {
		t.Fatal("identical fact duplicated")
	}
	s.now = func() time.Time { return day1.Add(48 * time.Hour) }
	second, changed, err := s.Write(ctx, Record{Kind: "fact", Project: "h96", Title: "порог кулера", Body: "кулер включается при 70 градусах днём"})
	if err != nil || !changed {
		t.Fatal(err)
	}
	hits, _ := s.Recall(ctx, Query{Text: "порог кулера"})
	if len(hits) != 1 || hits[0].ID != second.ID {
		t.Fatalf("current fact: %+v", hits)
	}
	asOf := day1.Add(time.Hour)
	hits, _ = s.Recall(ctx, Query{Text: "порог кулера", AsOf: &asOf})
	if len(hits) != 1 || hits[0].ID != first.ID {
		t.Fatalf("fact as of 08.11: %+v", hits)
	}
	old, _ := s.Get(ctx, first.ID)
	if old.SupersededBy != second.ID || old.ValidTo == nil {
		t.Fatalf("old fact not closed: %+v", old)
	}
}

func TestRemindersDue(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if _, _, err := s.Write(ctx, Record{Kind: "reminder", Title: "проверить Drive"}); err == nil {
		t.Fatal("reminder without time accepted")
	}
	at := now.Add(7 * 24 * time.Hour)
	if _, _, err := s.Write(ctx, Record{Kind: "reminder", Title: "проверить Drive", RemindAt: &at}); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.Due(ctx, true); len(due) != 0 {
		t.Fatal("reminder due early")
	}
	s.now = func() time.Time { return at.Add(time.Minute) }
	due, err := s.Due(ctx, true)
	if err != nil || len(due) != 1 || due[0].Title != "проверить Drive" {
		t.Fatal(due, err)
	}
	if again, _ := s.Due(ctx, true); len(again) != 0 {
		t.Fatal("reminder delivered twice")
	}
}

func TestCloseRemindersBySource(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for _, r := range []Record{
		{Kind: "reminder", Title: "Автовыпуск 0.11.10", Source: "release_queue/0.11.10", RemindAt: &now},
		{Kind: "reminder", Title: "проверить Drive", Source: "owner", RemindAt: &now},
	} {
		if _, _, err := s.Write(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseReminders(ctx, ""); err == nil {
		t.Fatal("empty prefix accepted")
	}
	if err := s.CloseReminders(ctx, "release_queue/"); err != nil {
		t.Fatal(err)
	}
	due, err := s.Due(ctx, false)
	if err != nil || len(due) != 1 || due[0].Title != "проверить Drive" {
		t.Fatal(due, err)
	}
}

func TestSyncDerivedIsWholeRebuild(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	if _, _, err := s.Write(ctx, Record{Kind: "note", Title: "своя запись про lease"}); err != nil {
		t.Fatal(err)
	}
	items := []Derived{{ID: "lesson-mcp-167", Kind: "lesson", Project: "mcpshka", Title: "MCP-167 мёртвые lease", Body: "lease на удалённый worktree"}}
	for i := 0; i < 2; i++ {
		if err := s.SyncDerived(ctx, "knowledge", items); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Count(ctx)
	if err != nil || n["knowledge"] != 1 || n["own"] != 1 {
		t.Fatal(n, err)
	}
	hits, _ := s.Recall(ctx, Query{Text: "lease"})
	if len(hits) != 2 {
		t.Fatalf("own and derived both searchable: %+v", hits)
	}
	if err = s.SyncDerived(ctx, "knowledge", nil); err != nil {
		t.Fatal(err)
	}
	if hits, _ = s.Recall(ctx, Query{Text: "lease"}); len(hits) != 1 {
		t.Fatalf("derived copy not replaced: %+v", hits)
	}
}

func TestRecallAnyLanguage(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	for _, r := range []Record{
		{Kind: "fact", Title: "路由器重启", Body: "路由器每天凌晨自动重启以清理内存", Source: "zh"},
		{Kind: "fact", Title: "テレビ設定", Body: "テレビはYouTubeをVPN経由で再生します", Source: "ja"},
		{Kind: "fact", Title: "Lüfter Steuerung", Body: "Der Lüfter schaltet sich bei hohen Temperaturen ein", Source: "de"},
		{Kind: "fact", Title: "Backup schedule", Body: "Encrypted backups are uploaded nightly to the cloud", Source: "en"},
	} {
		if _, _, err := s.Write(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string]string{
		"路由器 重启": "zh", "内存": "zh", "テレビ": "ja", "YouTube": "ja",
		"Temperatur Lüftern": "de", "backup uploads": "en",
	} {
		hits, err := s.Recall(ctx, Query{Text: q})
		if err != nil || len(hits) == 0 || hits[0].Source != want {
			t.Fatalf("%q: want %s, got %+v %v", q, want, hits, err)
		}
	}
}

func TestBriefAndClosingReminder(t *testing.T) {
	dir := t.TempDir()
	if Brief(context.Background(), dir) != "" {
		t.Fatal("brief created a memory")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	if _, _, err = s.Write(ctx, Record{Kind: "reminder", Title: "опубликовать Drive", RemindAt: &past}); err != nil {
		t.Fatal(err)
	}
	if b := Brief(ctx, dir); !strings.Contains(b, "напомнить: 1 — опубликовать Drive") {
		t.Fatal(b)
	}
	if _, changed, err := s.Write(ctx, Record{Kind: "reminder", Title: "опубликовать Drive", Reminded: true}); err != nil || !changed {
		t.Fatal("reminder not closable", err)
	}
	if b := Brief(ctx, dir); strings.Contains(b, "напомнить") {
		t.Fatal(b)
	}
}

func TestImportMarkdownCoverage(t *testing.T) {
	src := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("MEMORY.md", "- index only\n")
	write("feedback-short.md", "---\nname: feedback-short-commands\ndescription: Termius рвёт длинные строки; длинное давать скриптом\nmetadata:\n  type: feedback\n---\nДлинные команды — в файл, владельцу `! bash путь`.\n")
	log := "---\nname: checkpoint\ndescription: журнал\n---\n"
	for i := 0; i < 3; i++ {
		log += "## 10.11 09:25 MSK\n" + strings.Repeat("Выпуск Lane прошёл, сеть не менялась. ", 150) + "\n\n"
	}
	write("checkpoint.md", log)
	s := store(t)
	rep, err := s.ImportMarkdown(context.Background(), src, "mcpshka")
	if err != nil || rep.Files != 2 || rep.Records < 4 || len(rep.Skipped) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	hits, _ := s.Recall(context.Background(), Query{Text: "длинные строки Termius"})
	if len(hits) == 0 || hits[0].Kind != "decision" || hits[0].Source != "claude-memory:feedback-short-commands" {
		t.Fatalf("%+v", hits)
	}
	again, _ := s.ImportMarkdown(context.Background(), src, "mcpshka")
	if again.Changed != 0 || again.Records != rep.Records {
		t.Fatalf("reimport not idempotent: %+v", again)
	}
}

// The MCP client sends only what it knows; server-assigned fields must not
// be required (0.11.0 rejected every memory_write).
func TestWriteSchemaRequiresOnlyClientFields(t *testing.T) {
	s, err := jsonschema.For[WriteInput](nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Properties["record"].Required, ","); got != "kind,title" {
		t.Fatalf("record requires %q", got)
	}
}
