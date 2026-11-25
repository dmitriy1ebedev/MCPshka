package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConsolidateFoldsDuplicatesAndSummarizesDay(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	today := time.Now()
	yesterday := time.Date(today.Year(), today.Month(), today.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -1)
	s.now = func() time.Time { return yesterday }
	for _, r := range []Record{
		{Kind: "lesson", Project: "h96", Title: "Кулер старый", Body: "Кулер включает автоматизация HA."},
		{Kind: "lesson", Project: "h96", Title: "Кулер", Body: "Кулер включает автоматизация HA."},
		{Kind: "decision", Project: "lane", Title: "Lane 1.3.1", Body: "Статус native из path doctor."},
	} {
		if _, _, err := s.Write(ctx, r); err != nil {
			t.Fatal(err)
		}
		yesterday = yesterday.Add(time.Minute)
	}
	s.now = time.Now
	rep, err := s.Consolidate(ctx)
	if err != nil || rep.Duplicates != 1 || !strings.HasPrefix(rep.Day, "Сводка дня ") {
		t.Fatalf("consolidate: %+v %v", rep, err)
	}
	hits, _ := s.Recall(ctx, Query{Text: "кулер автоматизация"})
	for _, h := range hits {
		if h.Title == "Кулер старый" {
			t.Fatalf("duplicate still current: %+v", hits)
		}
	}
	sum, ok := s.LastSummary(ctx)
	if !ok || !strings.Contains(sum.Body, "lane: Lane 1.3.1") || !strings.Contains(sum.Body, "h96: Кулер") {
		t.Fatalf("summary: %+v %v", sum, ok)
	}
	again, err := s.Consolidate(ctx)
	if err != nil || again.Duplicates != 0 {
		t.Fatalf("second run must change nothing: %+v %v", again, err)
	}
	want := 3 // two current records and the day summary
	if today.Weekday() == time.Monday {
		want++ // and the week summary
	}
	if n, _ := s.Count(ctx); n["own"] != want {
		t.Fatalf("own records after two runs: %v", n)
	}
}

// Monday: the week summary repeats the day summary; neither is folded.
func TestConsolidateKeepsDayAndWeekSummariesOnMonday(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	monday := time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local)
	s.now = func() time.Time { return monday.AddDate(0, 0, -1) }
	if _, _, err := s.Write(ctx, Record{Kind: "lesson", Project: "h96", Title: "Кулер", Body: "Кулер включает HA."}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return monday }
	if rep, err := s.Consolidate(ctx); err != nil || rep.Week == "" {
		t.Fatalf("first: %+v %v", rep, err)
	}
	if again, err := s.Consolidate(ctx); err != nil || again.Duplicates != 0 {
		t.Fatalf("summaries folded: %+v %v", again, err)
	}
	if sum, ok := s.LastSummary(ctx); !ok || !strings.HasPrefix(sum.Title, "Сводка дня ") {
		t.Fatalf("day summary lost: %+v %v", sum, ok)
	}
}
