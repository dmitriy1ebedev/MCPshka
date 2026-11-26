package memory

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestQuestionsMakeParaphrasesFindable: a rule worded one way is found by the
// owner's other words once its questions are set (doc2query, 0.11.3 item 2).
func TestQuestionsMakeParaphrasesFindable(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	if _, _, err := s.Write(ctx, Record{Kind: "decision", Title: "Устройства через Home Assistant", Body: "Недоступное устройство сначала включить через ha_service_call.", Source: "rule/ha"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Write(ctx, Record{Kind: "fact", Title: "Свет в коридоре", Body: "Лампа меняется раз в год."}); err != nil {
		t.Fatal(err)
	}
	top := func(q string) string {
		hits, err := s.Recall(ctx, Query{Text: q, Limit: 3})
		if err != nil || len(hits) == 0 {
			return ""
		}
		return hits[0].Source
	}
	if top("умный дом телевизор молчит") == "rule/ha" {
		t.Fatal("fixture: the paraphrase must miss before questions")
	}
	if err := s.SetQuestions(ctx, map[string][]string{"rule/ha": {"телевизор не отвечает", "умный дом", "как включить свет"}}); err != nil {
		t.Fatal(err)
	}
	if got := top("умный дом телевизор молчит"); got != "rule/ha" {
		t.Fatalf("questions must make the paraphrase find the rule, got %q", got)
	}
	// Questions reindex only their own record: the other one keeps its index row.
	if hits, err := s.Recall(ctx, Query{Text: "лампа в коридоре", Limit: 1}); err != nil || len(hits) != 1 || hits[0].Title != "Свет в коридоре" {
		t.Fatalf("partial reindex lost another record: %v %v", hits, err)
	}
	// A new version of the record keeps the questions (kept by source).
	if _, _, err := s.Write(ctx, Record{Kind: "decision", Title: "Устройства через Home Assistant", Body: "Сначала ha_service_call, потом владелец.", Source: "rule/ha"}); err != nil {
		t.Fatal(err)
	}
	if got := top("умный дом телевизор молчит"); got != "rule/ha" {
		t.Fatalf("questions lost on supersede: %q", got)
	}
	left, err := s.WithoutQuestions(ctx, 10)
	if err != nil || len(left) != 0 {
		t.Fatalf("records without questions: %v %v", left, err)
	}
}

func TestMCP252SourceQuestionsBeatUnrelatedMatches(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	const question = "почему приставка выкидывает меня из панели роутера"
	const rule = "policy/router-admin"
	for _, rec := range []Record{
		{Kind: "decision", Title: "Опрос TP-Link BE900", Body: "Одна административная сессия. Опрос только раз в час.", Source: rule},
		{Kind: "fact", Title: "Панель роутера на приставке", Body: "Панель открывается в браузере приставки.", Source: "notes/browser"},
	} {
		if _, _, err := s.Write(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	top := func() string {
		t.Helper()
		hits, err := s.Recall(ctx, Query{Text: question, Limit: 1})
		if err != nil || len(hits) != 1 {
			t.Fatalf("expected one ranked match: %v, %v", hits, err)
		}
		return hits[0].Source
	}
	if got := top(); got != "notes/browser" {
		t.Fatalf("fixture must have an unrelated top hit before indexing the question: %q", got)
	}
	if err := s.SetQuestions(ctx, map[string][]string{rule: {question}}); err != nil {
		t.Fatal(err)
	}
	if got := top(); got != rule {
		t.Fatalf("the administrative session rule must rank first: got %q", got)
	}
}

// TestImportRefusesUnreadableAndArchivesOldLog: MCP-168 and item 3.
func TestImportRefusesUnreadableAndArchivesOldLog(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	if out, err := s.ImportMarkdown(ctx, filepath.Join(t.TempDir(), "missing"), "p"); err == nil || out.State != "no_effect" || out.Blocker != "memory_import_source_unavailable" {
		t.Fatalf("missing source: %+v %v", out, err)
	}
	empty := t.TempDir()
	if out, err := s.ImportMarkdown(ctx, empty, "p"); err != nil || out.State != "idle" || out.Effect {
		t.Fatalf("empty source: %+v %v", out, err)
	}
	dir := t.TempDir()
	big := strings.Repeat("слово ", 700)
	log := "---\nname: run-checkpoint\ndescription: журнал\ntype: project\n---\n\n## Старый блок\n" + big + "\n\n## Новый блок\nсейчас делаем выпуск " + big
	if err := os.WriteFile(filepath.Join(dir, "run-checkpoint.md"), []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := s.ImportMarkdown(ctx, dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Archived == 0 {
		t.Fatalf("older log sections must be archived: %+v", rep)
	}
	// Re-cut sections close the stale ones.
	if err := os.WriteFile(filepath.Join(dir, "run-checkpoint.md"), []byte("---\nname: run-checkpoint\ntype: project\n---\nкоротко"), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err = s.ImportMarkdown(ctx, dir, "p")
	if err != nil || rep.Closed == 0 {
		t.Fatalf("stale sections must close: %+v %v", rep, err)
	}
}

// fakeNavec builds a tiny pack: words on two axes (home vs code).
func fakeNavec(t *testing.T) []byte {
	words := []string{"дом", "ассистент", "лампа", "свет", "код", "сборка", "тест", "компиляция"}
	counts := []uint32{50, 40, 30, 30, 50, 40, 30, 30}
	var vocab bytes.Buffer
	_ = binary.Write(&vocab, binary.LittleEndian, uint32(len(words)))
	for _, c := range counts {
		_ = binary.Write(&vocab, binary.LittleEndian, c)
	}
	vocab.WriteString(strings.Join(words, "\n"))
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(vocab.Bytes())
	zw.Close()
	// dim 4, qdim 2, 2 centroids, sub 2: centroid 0 = home axis, 1 = code axis.
	var pq bytes.Buffer
	for _, v := range []uint32{uint32(len(words)), 4, 2, 2} {
		_ = binary.Write(&pq, binary.LittleEndian, v)
	}
	for i := range words {
		c := byte(0)
		if i >= 4 {
			c = 1
		}
		pq.Write([]byte{c, c})
	}
	codes := []float32{1, 0, 0, 1, 1, 0, 0, 1} // q0: c0=(1,0) c1=(0,1); q1 same
	for _, f := range codes {
		_ = binary.Write(&pq, binary.LittleEndian, math.Float32bits(f))
	}
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, f := range []struct {
		name string
		data []byte
	}{{"meta.json", []byte(`{"id":"test"}`)}, {"vocab.bin", gz.Bytes()}, {"pq.bin", pq.Bytes()}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0600, Size: int64(len(f.data))})
		tw.Write(f.data)
	}
	tw.Close()
	return out.Bytes()
}

// TestMeaningLayerFindsSynonyms: with the word table a query that shares no
// word with the record still finds it by meaning (item 4).
func TestMeaningLayerFindsSynonyms(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	dir := filepath.Dir(s.file)
	if n, err := buildVectors(fakeNavec(t), dir, 100); err != nil || n != 8 {
		t.Fatalf("build: %d %v", n, err)
	}
	if _, _, err := s.Write(ctx, Record{Kind: "decision", Title: "Ассистент дома", Body: "лампа"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Write(ctx, Record{Kind: "decision", Title: "Сборка по одной", Body: "компиляция"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Recall(ctx, Query{Text: "свет", Limit: 3})
	if err != nil || len(hits) == 0 || hits[0].Title != "Ассистент дома" || !strings.Contains(hits[0].Why, "смысл") {
		t.Fatalf("meaning must find the home record: %+v %v", hits, err)
	}
	if err := s.Reindex(ctx); err != nil {
		t.Fatal(err)
	}
	hits, _ = s.Recall(ctx, Query{Text: "тест", Limit: 3})
	if len(hits) == 0 || hits[0].Title != "Сборка по одной" {
		t.Fatalf("after reindex: %+v", hits)
	}
}

func TestExamIsWellFormed(t *testing.T) {
	e, err := BuiltinExam()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	if len(e.Questions) < 50 {
		t.Fatalf("exam needs ≥50 questions, has %d", len(e.Questions))
	}
	for _, q := range e.Questions {
		if q.Q == "" || len(q.Expect) == 0 || seen[q.Q] {
			t.Fatalf("bad or duplicate question %+v", q)
		}
		seen[q.Q] = true
	}
}
