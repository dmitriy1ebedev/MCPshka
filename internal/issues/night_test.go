package issues

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerdicts(t *testing.T) {
	now := time.Date(2026, 11, 16, 3, 0, 0, 0, time.Local)
	in := NightInput{Now: now, Prod: "0.13.3", Findings: []Finding{{Kind: "service", Subject: "smbd.service"}},
		Agent: func(task string) string { return map[string]string{"aaaa": "failed", "bbbb": "running"}[task] }}
	fresh := now.Add(-time.Hour)
	for _, c := range []struct {
		i    Issue
		want string
	}{
		{Issue{Status: Open, Source: "sentinel:service:smbd.service", UpdatedAt: fresh}, NightStill},
		{Issue{Status: Open, Source: "sentinel:service:dnsmasq.service", UpdatedAt: fresh}, NightGone},
		{Issue{Status: Fixed, Release: "0.13.3", Source: "sentinel:service:x", UpdatedAt: fresh}, NightAwaitAccept},
		{Issue{Status: Fixed, Release: "0.13.10", UpdatedAt: fresh}, NightAwaitRelease},
		{Issue{Status: Fixed, UpdatedAt: fresh}, NightAwaitRelease},
		{Issue{Status: InProgress, AgentTask: "aaaa", UpdatedAt: fresh}, NightAgentDone},
		{Issue{Status: InProgress, AgentTask: "bbbb", UpdatedAt: fresh}, NightOpen},
		{Issue{Status: Open, UpdatedAt: now.Add(-8 * 24 * time.Hour)}, NightStale},
	} {
		if got := Verdict(c.i, in); got != c.want {
			t.Fatalf("%+v: %s want %s", c.i, got, c.want)
		}
	}
	if versionLess("0.13.10", "0.13.9") || !versionLess("0.13.9", "0.13.10") || versionLess("x", "0.1.0") {
		t.Fatal("versionLess")
	}
}

// A card hears about a verdict once; the next night with the same verdict
// keeps its first date and adds nothing.
func TestNightReviewAppendsOnlyOnChange(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "issues.json"))
	i, err := s.Create(NewIssue{Title: "service smbd.service: failed", Priority: "P1", Body: "x", Source: "sentinel:service:smbd.service"}, "t")
	if err != nil {
		t.Fatal(err)
	}
	night := func(day int, findings []Finding) NightReport {
		r, err := s.NightReview(NightInput{Now: time.Date(2026, 11, day, 3, 0, 0, 0, time.Local), Findings: findings})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	night(16, nil)
	night(17, nil)
	got, _ := s.Get(i.ID)
	if n := strings.Count(got.Body, "Ночной разбор"); n != 1 || !strings.Contains(got.Body, "Ночной разбор 16.11:** симптом исчез") {
		t.Fatalf("%d lines:\n%s", n, got.Body)
	}
	r := night(18, []Finding{{Kind: "service", Subject: "smbd.service"}})
	got, _ = s.Get(i.ID)
	if r.Cards[0].Verdict != NightStill || r.Cards[0].Since != "18.11" || !strings.Contains(got.Body, "18.11:** симптом держится") {
		t.Fatalf("%+v\n%s", r.Cards, got.Body)
	}
	line := NightBrief(ReadNight(NightPath(filepath.Join(filepath.Dir(s.path), "issues.json"))), time.Date(2026, 11, 18, 9, 0, 0, 0, time.Local))
	if !strings.Contains(line, "карточек 1") || !strings.Contains(line, "симптом держится 1 ("+i.ID+")") {
		t.Fatal(line)
	}
	if NightBrief(r, time.Date(2026, 11, 20, 9, 0, 0, 0, time.Local)) != "" {
		t.Fatal("stale report briefed")
	}
}

func TestNightDue(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	at := func(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, loc) }
	for _, c := range []struct {
		name string
		now  time.Time
		last time.Time
		load float64
		want bool
	}{
		{"21:59", at(16, 21, 59), time.Time{}, 0, false},
		{"22:00", at(16, 22, 0), time.Time{}, 0, true},
		{"23:30", at(16, 23, 30), time.Time{}, 0, true},
		{"00:00", at(17, 0, 0), time.Time{}, 0, true},
		{"03:00", at(17, 3, 0), time.Time{}, 0, true},
		{"05:59", at(17, 5, 59), time.Time{}, 0, true},
		{"06:00", at(17, 6, 0), time.Time{}, 0, false},
		{"12:00", at(17, 12, 0), time.Time{}, 0, false},
		{"busy evening", at(16, 23, 30), time.Time{}, 2, false},
		{"busy morning", at(17, 3, 0), time.Time{}, 2, false},
		{"last hour starts", at(17, 5, 0), time.Time{}, 2, true},
		{"busy last hour", at(17, 5, 59), time.Time{}, 2, true},
		{"repeat evening", at(16, 23, 30), at(16, 22, 0), 0, false},
		{"repeat after midnight", at(17, 3, 0), at(16, 23, 30).UTC(), 0, false},
		{"repeat morning", at(17, 5, 59), at(17, 3, 0), 2, false},
		{"previous night", at(17, 3, 0), at(16, 3, 0), 0, true},
		{"new night same date", at(17, 22, 0), at(17, 3, 0), 0, true},
		{"before start", at(17, 3, 0), at(16, 21, 59), 0, true},
		{"year boundary", time.Date(2027, 1, 1, 3, 0, 0, 0, loc), time.Date(2026, 12, 31, 22, 0, 0, 0, loc), 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := NightDue(c.now, c.last, c.load); got != c.want {
				t.Fatalf("NightDue(%v, %v, %v) = %v, want %v", c.now, c.last, c.load, got, c.want)
			}
		})
	}
}
