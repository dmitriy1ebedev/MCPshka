package autonomy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/statestore"
	"mcpshka/internal/workmetrics"
)

func TestBaselineIsImmutableForUTCDayAndProject(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	src := workmetrics.Sources{DB: db}
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	p := workmetrics.Period{Since: now.Add(-time.Hour), Until: now, StuckSeconds: 900}
	record := func(p workmetrics.Period, now time.Time) Result {
		t.Helper()
		r, err := save(context.Background(), src, p, now)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := record(p, now)
	p.Since = now.Add(-2 * time.Hour)
	replayed := record(p, now.Add(time.Minute))
	if replayed.BaselineDay != "2026-11-26" || !replayed.Metrics.At.Equal(first.Metrics.At) || !replayed.Metrics.Since.Equal(first.Metrics.Since) {
		t.Fatalf("baseline changed: %+v", replayed)
	}
	p.Project = "/root/repos/demo"
	if scoped := record(p, now); scoped.Metrics.Project != p.Project {
		t.Fatalf("project not isolated: %+v", scoped)
	}
	if next := record(p, now.Add(24*time.Hour)); next.BaselineDay != "2026-11-27" {
		t.Fatalf("date not isolated: %+v", next)
	}
}

func TestWindowRejectsFutureAndInvertedDates(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, in := range []Window{{Until: "2026-11-27T00:00:00Z"}, {Since: "2026-11-26T12:00:00Z"}, {StuckSeconds: -1}} {
		if _, err := in.period(nil, now); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	p, err := (Window{}).period(nil, now)
	if err != nil || p.StuckSeconds != 900 || !p.Since.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("default period: %+v %v", p, err)
	}
}

func TestMutationProjectExtractsOnlyRepositoryPath(t *testing.T) {
	for _, input := range []string{"/etc/private", "/root/repos/demo?token=123", "/root/repos/../private"} {
		if p := MutationProject(map[string]any{"path": input}); p != "" {
			t.Errorf("unexpected path %q", p)
		}
	}
	if actual := MutationProject(map[string]any{"path": "/root/repos/demo/internal/file.go"}); !strings.HasSuffix(actual, "/repos/demo") {
		t.Fatalf("project was not extracted: %q", actual)
	}
}
