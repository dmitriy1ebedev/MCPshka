package roadmaprun

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecentKeepsActiveAcrossProjectsAndBoundsHistory(t *testing.T) {
	dir := withStateDir(t)
	now := time.Now()
	c := Config{Now: func() time.Time { return now }}
	for i := 0; i < 25; i++ {
		j := Journal{ID: fmt.Sprint(i), Path: "/p", State: "done", StartedAt: now.Add(time.Duration(i) * time.Minute)}
		if err := c.save(&j); err != nil {
			t.Fatal(err)
		}
	}
	j := Journal{ID: "active", Path: "/other", State: "running", Goal: "repair", Step: "agent", Agent: "codex", StartedAt: now.Add(-time.Hour)}
	if err := c.save(&j); err != nil {
		t.Fatal(err)
	}
	runs, truncated, err := Recent("", time.Time{})
	if err != nil || truncated || len(runs) != 21 || runs[0].ID != "active" || runs[0].Goal != "repair" || runs[0].Step != "agent" {
		t.Fatalf("%+v %v %v", runs, truncated, err)
	}
	filtered, _, _ := Recent("/other", time.Time{})
	if len(filtered) != 1 {
		t.Fatal(filtered)
	}
	_ = os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0600)
	_, truncated, _ = Recent("", time.Time{})
	if !truncated {
		t.Fatal("hidden unknown journal")
	}
	_ = os.WriteFile(filepath.Join(dir, "large.json"), []byte(strings.Repeat("x", (1<<20)+1)), 0600)
	if _, err := ReadJournal(filepath.Join(dir, "large.json"), time.Time{}); err == nil {
		t.Fatal("oversized journal accepted")
	}
}
func TestRunnerJournalRedactsItsLease(t *testing.T) {
	dir := withStateDir(t)
	c := Config{secret: "holder", Now: time.Now}
	j := Journal{ID: "private", Blocker: "failed holder", NextAction: "password=hidden", Phases: []Phase{{Note: "Authorization: Bearer token-value"}}}
	if err := c.save(&j); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "private.json"))
	for _, s := range []string{"holder", "hidden", "token-value"} {
		if strings.Contains(string(raw), s) {
			t.Fatal("secret leaked")
		}
	}
}
