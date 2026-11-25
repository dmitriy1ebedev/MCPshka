package agents

import (
	"testing"
	"time"
)

func TestParseResetDatedCodexLimit(t *testing.T) {
	now := time.Date(2026, 11, 2, 19, 41, 0, 0, time.Local)
	r := parseReset("ERROR: You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/settings/usage to purchase more credits or try again at Nov 6th, 2026 10:20 AM.", now)
	if r.Year() != 2026 || r.Month() != time.November || r.Day() != 6 || r.Hour() != 10 || r.Minute() != 20 {
		t.Fatal("dated codex reset", r)
	}
	if r := parseReset("try again at Oct 30th, 2026 10:20 PM", now); !r.IsZero() {
		t.Fatal("past dated reset must be unknown, got", r)
	}
	if r := parseReset("try again at Dec 1st, 2026 12:05 PM", now); r.Hour() != 12 || r.Day() != 1 {
		t.Fatal("noon reset", r)
	}
}
