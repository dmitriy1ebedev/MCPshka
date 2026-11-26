package issues

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# Реестр

## Часть A

### MCP-001 · Автономность · P0
**Статус:** подтверждено 28.10
**Проблема:** Исполнитель заканчивает работу после PASS тестов, коммита или сборки. Дальше текст.
**Ожидаемо:** до конца.

### MCP-053 · Наблюдаемость · P1
**Проблема:** Нет журнала исполнителей для владельца в одном месте.

## Дополнение r22

### MCP-112 · P1 · Страница Face ID показывает «no such file or directory» после успешного подтверждения
**Суть.** approved_task: страница ответила ошибкой.

### MCP-053 · Наблюдаемость · P1
**Проблема:** Второй блок той же карточки.

### Подтверждённые состояния
не карточка
`

func TestImportBothHeadingFormatsAndMerge(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "issues.json"))
	res, err := s.Import(sample, "r20", "import")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 3 || res.Total != 3 {
		t.Fatalf("import %+v", res)
	}
	a, _ := s.Get("MCP-001")
	if a.Priority != "P0" || a.Category != "Автономность" || !strings.HasPrefix(a.Title, "Исполнитель заканчивает") || a.StatusNote != "подтверждено 28.10" || a.Status != Open {
		t.Fatalf("format A %+v", a)
	}
	b, _ := s.Get("mcp-112")
	if b.Priority != "P1" || !strings.HasPrefix(b.Title, "Страница Face ID") || b.Category != "" {
		t.Fatalf("format B %+v", b)
	}
	d, _ := s.Get("MCP-053")
	if !strings.Contains(d.Body, "Второй блок") || strings.Contains(d.Body, "не карточка") {
		t.Fatalf("merge/stop %q", d.Body)
	}
	again, _ := s.Import(sample, "r20", "import")
	if len(again.Added) != 0 || len(again.Skipped) != 3 {
		t.Fatalf("re-import overwrote: %+v", again)
	}
	n, err := s.Create(NewIssue{Title: "Новая", Priority: "p2", Body: "x"}, "ai")
	if err != nil || n.ID != "MCP-113" {
		t.Fatalf("next id %v %v", n.ID, err)
	}
}

func TestLifecycleClosesOnlyByVerifiedAcceptance(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "issues.json"))
	i, err := s.Create(NewIssue{Title: "Брокер не успевает восстановление executor", Priority: "P0", Body: "deadline"}, "ai")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(i.ID, Patch{Status: Closed}, "ai"); err == nil {
		t.Fatal("closed without acceptance")
	}
	if _, err := s.Update(i.ID, Patch{Status: Fixed}, "ai"); err == nil {
		t.Fatal("fixed without commit")
	}
	if _, err := s.Update(i.ID, Patch{Status: Rejected}, "ai"); err == nil {
		t.Fatal("rejected without note")
	}
	if _, err := s.Accept(i.ID, "job:x", "", "ai", func(Issue, string) error { return nil }); err == nil {
		t.Fatal("accepted an open card")
	}
	if _, err := s.Update(i.ID, Patch{Status: Fixed, Commit: "abc1234", Release: "0.7.0"}, "ai"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(i.ID, "job:bad", "", "ai", func(Issue, string) error { return errors.New("job failed") }); err == nil {
		t.Fatal("unverified evidence closed")
	}
	got, err := s.Accept(i.ID, "job:ok", "регрессия", "owner", func(Issue, string) error { return nil })
	if err != nil || got.Status != Closed || got.Acceptance == nil || got.Acceptance.By != "owner" {
		t.Fatalf("accept %+v %v", got, err)
	}
	reopened, err := s.Update(i.ID, Patch{Status: Open, Note: "вернулось"}, "owner")
	if err != nil || reopened.Acceptance != nil {
		t.Fatalf("reopen %+v %v", reopened, err)
	}
	list, counts, _ := s.List(Filter{Status: "active", Priority: "P0"})
	if len(list) != 1 || counts[Open] != 1 {
		t.Fatalf("list %v %v", list, counts)
	}
	similar, _ := s.Similar("брокер восстановление executor таймаут", 3)
	if len(similar) != 1 || similar[0].ID != i.ID {
		t.Fatalf("similar %v", similar)
	}
	if found, _, _ := s.List(Filter{Text: "восстановление брокер"}); len(found) != 1 {
		t.Fatalf("text filter %v", found)
	}
}
