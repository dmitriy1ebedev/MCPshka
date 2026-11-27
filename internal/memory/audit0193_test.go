package memory

import (
	"context"
	"testing"
)

// 26.11 audit records: tag-only recall listed nothing ("query needs words"),
// and five records of one chat sharing a source read back one question set.
func TestTagOnlyRecallAndQuestionsPerTopic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := "Чат UG2 26.11"
	for i, title := range []string{"Счётчик продолжений", "Ошибки схем", "Правовой разбор"} {
		r := Record{Kind: "lesson", Title: title, Body: "разбор " + title, Tags: []string{"аудит-0193"}, Source: src,
			Questions: []string{"вопрос про " + title}}
		if i == 2 {
			r.Tags = []string{"другое"}
		}
		if _, _, err := s.Write(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.Recall(ctx, Query{Tag: "аудит-0193", Limit: 20})
	if err != nil || len(hits) != 2 {
		t.Fatal("tag-only recall:", len(hits), err)
	}
	if _, err := s.Recall(ctx, Query{}); err == nil {
		t.Fatal("no words and no tag must still be refused")
	}
	seen := map[string]bool{}
	for _, h := range hits {
		r, err := s.Inspect(ctx, h.ID)
		if err != nil || len(r.Questions) != 1 || r.Questions[0] != "вопрос про "+r.Title {
			t.Fatalf("questions of %q: %v %v", r.Title, r.Questions, err)
		}
		seen[r.Source] = true
	}
	if len(seen) != 2 {
		t.Fatal("topics sharing a source must keep separate question keys", seen)
	}
}
