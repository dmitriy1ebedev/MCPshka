package memory

import (
	"context"
	"testing"
	"time"
)

func TestAuditOptimizeUndo(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	old := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	now := old.Add(20 * 24 * time.Hour)
	write := func(at time.Time, r Record) Record {
		s.now = func() time.Time { return at }
		rec, _, err := s.Write(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	stale := write(old, Record{Kind: "fact", Title: "checkpoint 39", Body: "main c401002 не влито lanelink", Source: "claude-memory:mcpshka-recovery-checkpoint#39.1"})
	fresh := write(now, Record{Kind: "fact", Title: "checkpoint 80", Body: "0.17.5 в проде", Source: "claude-memory:mcpshka-recovery-checkpoint#80"})
	dupOld := write(old, Record{Kind: "note", Title: "ssh тоннель A", Body: "Тоннель поднимается через lane и проверяется table 110 после перезапуска", Source: "x"})
	dupNew := write(now, Record{Kind: "note", Title: "ssh тоннель B", Body: "тоннель поднимается через  lane и проверяется table 110 после перезапуска", Source: "y"})
	r16 := write(old, Record{Kind: "fact", Title: "MCPshka 0.16.7 в проде 19.11", Body: "выпуск", Source: "release"})
	r17 := write(now, Record{Kind: "fact", Title: "MCPshka 0.17.4 в проде", Body: "выпуск", Source: "release"})
	s.now = func() time.Time { return now }
	rep, err := s.Audit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{stale.ID: "archive", dupOld.ID: "close", r16.ID: "close"}
	if len(rep.Plan) != len(want) || rep.PlanSHA == "" {
		t.Fatalf("plan = %+v", rep.Plan)
	}
	for _, p := range rep.Plan {
		if want[p.ID] != p.Op {
			t.Fatalf("unexpected op %+v", p)
		}
	}
	if _, err := s.Optimize(ctx, "deadbeef"+rep.PlanSHA[8:]); err == nil {
		t.Fatal("foreign plan applied")
	}
	res, err := s.Optimize(ctx, rep.PlanSHA)
	if err != nil || res.Applied != 3 {
		t.Fatalf("optimize %+v %v", res, err)
	}
	if r, _ := s.Get(ctx, stale.ID); r.Kind != "archive" {
		t.Fatalf("stale section kind %s", r.Kind)
	}
	if r, _ := s.Get(ctx, r16.ID); r.ValidTo == nil || r.SupersededBy != r17.ID {
		t.Fatalf("old release not superseded: %+v", r)
	}
	if r, _ := s.Get(ctx, dupNew.ID); r.ValidTo != nil {
		t.Fatal("newest duplicate closed")
	}
	if again, _ := s.Audit(ctx); len(again.Plan) != 0 {
		t.Fatalf("plan not empty after optimize: %+v", again.Plan)
	}
	if _, err := s.Undo(ctx, res.Op); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Get(ctx, stale.ID); r.Kind != "fact" || r.ValidTo != nil {
		t.Fatalf("undo did not restore: %+v", r)
	}
	if r, _ := s.Get(ctx, r16.ID); r.ValidTo != nil || r.SupersededBy != "" {
		t.Fatalf("undo did not reopen: %+v", r)
	}
	if _, err := s.Undo(ctx, res.Op); err == nil {
		t.Fatal("second undo accepted")
	}
	_ = fresh
}
