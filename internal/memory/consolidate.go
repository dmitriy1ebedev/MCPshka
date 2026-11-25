package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Night consolidation (0.11.2): fold duplicate own records into the newest
// one, and write a day summary (and on Mondays a week summary) of what was
// recorded, so a new chat sees «что было вчера» in one record.

type ConsolidateReport struct {
	Duplicates int    `json:"duplicates_closed"`
	Day        string `json:"day_summary,omitempty"`
	Week       string `json:"week_summary,omitempty"`
}

const summaryTag = "сводка"

// Consolidate is idempotent: a second run the same night changes nothing.
func (s *Store) Consolidate(ctx context.Context) (ConsolidateReport, error) {
	now := s.now()
	var rep ConsolidateReport
	// 1. Same kind, project and body under different titles: keep the newest.
	// Summaries are never folded: on Monday the week summary may repeat the
	// day summary word for word, and both must stay.
	notSummary := func(t string) string {
		return " AND (" + t + ".tags IS NULL OR instr(" + t + ".tags," + lit(summaryTag) + ")=0)"
	}
	rows, err := s.exec(ctx, `SELECT a.id AS old, (SELECT b.id FROM mem b WHERE b.valid_to IS NULL AND b.origin='own' AND b.kind=a.kind
 AND ifnull(b.project,'')=ifnull(a.project,'') AND b.body=a.body AND b.body<>''`+notSummary("b")+` ORDER BY b.recorded_at DESC, b.rid DESC LIMIT 1) AS keep
 FROM mem a WHERE a.valid_to IS NULL AND a.origin='own' AND a.kind<>'reminder' AND a.body<>''`+notSummary("a")+` LIMIT 2000;`)
	if err != nil {
		return rep, err
	}
	var b strings.Builder
	stamp := now.UTC()
	for _, r := range rows {
		if old, keep := str(r, "old"), str(r, "keep"); keep != "" && old != keep {
			b.WriteString("UPDATE mem SET valid_to=" + litTime(&stamp) + ", superseded_by=" + lit(keep) + " WHERE id=" + lit(old) + " AND valid_to IS NULL;\n")
			rep.Duplicates++
		}
	}
	if rep.Duplicates > 0 {
		if _, err := s.exec(ctx, "BEGIN IMMEDIATE;\n"+b.String()+"COMMIT;"); err != nil {
			return rep, err
		}
	}
	// 2. Yesterday's summary; on Monday also the past week.
	local := now.Local()
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	if rep.Day, err = s.summarize(ctx, "Сводка дня "+today.AddDate(0, 0, -1).Format("2006-01-02"), today.AddDate(0, 0, -1), today); err != nil {
		return rep, err
	}
	if today.Weekday() == time.Monday {
		if rep.Week, err = s.summarize(ctx, "Сводка недели "+today.AddDate(0, 0, -7).Format("2006-01-02"), today.AddDate(0, 0, -7), today); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (s *Store) summarize(ctx context.Context, title string, from, to time.Time) (string, error) {
	f, t := from.UTC(), to.UTC()
	rows, err := s.exec(ctx, "SELECT kind, project, title FROM mem WHERE origin='own' AND kind<>'reminder' AND superseded_by IS NULL AND (tags IS NULL OR instr(tags,"+lit(summaryTag)+")=0) AND recorded_at>="+litTime(&f)+" AND recorded_at<"+litTime(&t)+" ORDER BY recorded_at LIMIT 200;")
	if err != nil || len(rows) == 0 {
		return "", err
	}
	byKind := map[string][]string{}
	for _, r := range rows {
		item := str(r, "title")
		if p := str(r, "project"); p != "" {
			item = p + ": " + item
		}
		byKind[str(r, "kind")] = append(byKind[str(r, "kind")], item)
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := []string{}
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s (%d): %s", k, len(byKind[k]), strings.Join(byKind[k], "; ")))
	}
	body := clean(strings.Join(parts, "\n"), maxBody)
	_, _, err = s.Write(ctx, Record{Kind: "note", Project: "mcpshka", Title: title, Body: body, Tags: []string{summaryTag}, Source: "memory consolidation", ValidFrom: to.UTC()})
	return title, err
}

// LastSummary is the newest day summary title and body (SessionStart INDEX).
func (s *Store) LastSummary(ctx context.Context) (Record, bool) {
	rows, err := s.exec(ctx, "SELECT * FROM mem WHERE origin='own' AND valid_to IS NULL AND kind='note' AND instr(tags,"+lit(summaryTag)+")>0 AND title LIKE 'Сводка дня %' ORDER BY title DESC LIMIT 1;")
	if err != nil || len(rows) == 0 {
		return Record{}, false
	}
	return fromRow(rows[0]), true
}

// Night runs Consolidate once a day after 03:00 local time, forever; report
// gets the outcome line. The server starts it once.
func Night(dir string, report func(string)) {
	last := ""
	for {
		now := time.Now()
		if day := now.Format("2006-01-02"); now.Hour() >= 3 && last != day {
			last = day
			if s, err := OpenExisting(dir); err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				rep, err := s.Consolidate(ctx)
				cancel()
				line := fmt.Sprintf("%s dup %d", rep.Day, rep.Duplicates)
				if err != nil {
					line = err.Error()
				}
				// New chat exports in memory-inbox are cut into chunks for
				// distillation (0.11.3 item 8).
				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
				if in, err := s.InboxScan(ctx); err == nil && in.Files > 0 {
					line += fmt.Sprintf("; inbox +%d files, pending %d", in.Files, in.Pending)
				}
				cancel()
				report(line)
			}
		}
		time.Sleep(10 * time.Minute)
	}
}
