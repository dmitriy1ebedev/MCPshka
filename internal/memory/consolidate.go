package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"mcpshka/internal/runoutcome"
	"mcpshka/internal/statestore"
)

// Night consolidation (0.11.2): fold duplicate own records into the newest
// one, and write a day summary (and on Mondays a week summary) of what was
// recorded, so a new chat sees «что было вчера» in one record.

type ConsolidateReport struct {
	runoutcome.Result
	Duplicates int    `json:"duplicates_closed"`
	Day        string `json:"day_summary,omitempty"`
	Week       string `json:"week_summary,omitempty"`
}

const summaryTag = "сводка"

// Consolidate is idempotent: a second run the same night changes nothing.
func (s *Store) Consolidate(ctx context.Context) (rep ConsolidateReport, runErr error) {
	now := s.now()
	processed := 0
	defer func() {
		blocker, next := "", ""
		if runErr != nil {
			blocker, next = "memory_consolidation_failed", "inspect memory storage and retry the consolidation"
		}
		rep.Result = runoutcome.Resolve(processed, false, blocker, next)
	}()
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
		rows, err := s.exec(ctx, "BEGIN IMMEDIATE;\n"+b.String()+"COMMIT;\nSELECT total_changes() AS changed;")
		if err != nil {
			rep.Duplicates = 0
			return rep, err
		}
		n, _ := rows[0]["changed"].(float64)
		rep.Duplicates = int(n)
		processed += rep.Duplicates
	}
	// 2. Yesterday's summary; on Monday also the past week.
	local := now.Local()
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	var changed bool
	if rep.Day, changed, err = s.summarize(ctx, "Сводка дня "+today.AddDate(0, 0, -1).Format("2006-01-02"), today.AddDate(0, 0, -1), today); err != nil {
		return rep, err
	}
	if changed {
		processed++
	}
	if today.Weekday() == time.Monday {
		if rep.Week, changed, err = s.summarize(ctx, "Сводка недели "+today.AddDate(0, 0, -7).Format("2006-01-02"), today.AddDate(0, 0, -7), today); err != nil {
			return rep, err
		}
		if changed {
			processed++
		}
	}
	return rep, nil
}

func (s *Store) summarize(ctx context.Context, title string, from, to time.Time) (string, bool, error) {
	f, t := from.UTC(), to.UTC()
	rows, err := s.exec(ctx, "SELECT kind, project, title FROM mem WHERE origin='own' AND kind<>'reminder' AND superseded_by IS NULL AND (tags IS NULL OR instr(tags,"+lit(summaryTag)+")=0) AND recorded_at>="+litTime(&f)+" AND recorded_at<"+litTime(&t)+" ORDER BY recorded_at LIMIT 200;")
	if err != nil || len(rows) == 0 {
		return "", false, err
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
	_, changed, err := s.Write(ctx, Record{Kind: "note", Project: "mcpshka", Title: title, Body: body, Tags: []string{summaryTag}, Source: "memory consolidation", ValidFrom: to.UTC()})
	return title, changed, err
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
func Night(dir string, db *statestore.DB, report func(string)) {
	last := ""
	for {
		now := time.Now()
		if day := now.Format("2006-01-02"); now.Hour() >= 3 && last != day {
			last = day
			s, err := OpenExisting(dir)
			if err != nil {
				out := runoutcome.Resolve(0, true, "memory_store_unavailable", "inspect access to the memory database")
				reportNight(db, "memory-night", day, now, out, err, report)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				rep, err := s.Consolidate(ctx)
				cancel()
				reportNight(db, "memory-night", day, now, rep.Result, err, report)

				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
				in, err := s.InboxScan(ctx)
				cancel()
				if err != nil && in.State == "" {
					in.Result = runoutcome.Resolve(0, true, "inbox_store_unavailable", "inspect the memory inbox database before retrying")
				}
				reportNight(db, "memory-inbox", day, now, in.Result, err, report)
			}
		}
		time.Sleep(10 * time.Minute)
	}
}

func reportNight(db *statestore.DB, source, day string, now time.Time, out runoutcome.Result, runErr error, report func(string)) {
	err := runoutcome.Record(db, source, day, out, now)
	report(fmt.Sprintf("%s %s processed=%d blocker=%s next=%s error=%v observation=%v", source, out.State, out.Processed, out.Blocker, out.NextSafeAction, runErr, err))
}
