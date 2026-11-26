package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestArchiveFenceAndAtomicReceipt(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	workers := [2]*Store{s, {file: s.file, now: time.Now}}
	type attempt struct {
		key string
		run ArchiveRun
		err error
	}
	ch := make(chan attempt, 2)
	for i, w := range workers {
		go func() {
			key := fmt.Sprintf("claim-%d", i)
			run, err := w.ArchiveClaim(ctx, "claude", "owner", key, true)
			ch <- attempt{key, run, err}
		}()
	}
	a, b := <-ch, <-ch
	if a.run.State == "busy" {
		a, b = b, a
	}
	if a.err != nil || b.err != nil || a.run.State != "running" || b.run.State != "busy" || a.run.ID != b.run.ID {
		t.Fatalf("two contenders: %+v %+v", a, b)
	}
	decision := ArchiveDecision{SHA: strings.Repeat("a", 64), Class: "lesson", Record: &Record{Kind: "lesson", Title: "Test lesson", Body: "Use a fenced checkpoint."}}
	if _, err := s.ArchiveCommit(ctx, "claude", "owner", b.key, decision); err == nil {
		t.Fatal("losing writer committed")
	}
	if _, err := s.exec(ctx, `CREATE TRIGGER fail_archive_receipt BEFORE INSERT ON archive_receipt BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveCommit(ctx, "claude", "owner", a.key, decision); err == nil {
		t.Fatal("injected checkpoint failure returned success")
	}
	if n, err := s.Count(ctx); err != nil || n["own"] != 0 {
		t.Fatalf("record escaped rollback: %v %v", n, err)
	}
	if _, err := s.exec(ctx, "DROP TRIGGER fail_archive_receipt;"); err != nil {
		t.Fatal(err)
	}
	first, err := s.ArchiveCommit(ctx, "claude", "owner", a.key, decision)
	if err != nil || first.RecordID == "" {
		t.Fatal(first, err)
	}
	restarted := workers[1]
	if again, err := restarted.ArchiveCommit(ctx, "claude", "owner", a.key, decision); err != nil || again != first {
		t.Fatalf("lost reply reconciliation: %+v %v", again, err)
	}
	if n, err := restarted.Count(ctx); err != nil || n["own"] != 1 {
		t.Fatalf("duplicate lesson: %v %v", n, err)
	}
	if _, err := s.exec(ctx, "UPDATE archive_run SET expires=0;"); err != nil {
		t.Fatal(err)
	}
	newRun, err := restarted.ArchiveClaim(ctx, "claude", "owner", "replacement", true)
	if err != nil || newRun.State != "running" || newRun.ID == a.run.ID {
		t.Fatal(newRun, err)
	}
	decision.SHA = strings.Repeat("b", 64)
	if _, err := s.ArchiveCommit(ctx, "claude", "owner", a.key, decision); err == nil {
		t.Fatal("expired writer committed")
	}
	if run, err := s.ArchiveFinish(ctx, "claude", "owner", "replacement", true, "cursor_blocked", "inspect pending SHA"); err != nil || run.State != "no_effect" || run.Processed != 0 {
		t.Fatal(run, err)
	}
}
