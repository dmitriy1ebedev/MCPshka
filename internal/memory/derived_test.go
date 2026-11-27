package memory

import (
	"context"
	"testing"
	"time"
)

func TestSourceChangeRemovesDerivedSummary(t *testing.T) {
	for _, action := range []string{"supersede", "forget", "erase"} {
		t.Run(action, func(t *testing.T) {
			s := store(t)
			ctx := context.Background()
			yesterday := time.Now().Add(-24 * time.Hour)
			s.now = func() time.Time { return yesterday }
			old, _, err := s.Write(ctx, Record{Kind: "fact", Title: "fixture", Body: "OLD_PRIVATE_FACT"})
			if err != nil {
				t.Fatal(err)
			}
			s.now = time.Now
			if _, err := s.Consolidate(ctx); err != nil {
				t.Fatal(err)
			}
			if _, ok := s.LastSummary(ctx); !ok {
				t.Fatal("no summary fixture")
			}
			if action == "supersede" {
				_, _, err = s.Write(ctx, Record{Kind: "fact", Title: "fixture", Body: "new fact"})
			} else {
				_, err = s.Forget(ctx, old.ID, action == "erase")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.LastSummary(ctx); ok {
				t.Fatal("stale summary survived")
			}
			rows, err := s.exec(ctx, "SELECT count(*) AS n FROM mem WHERE source='memory consolidation';")
			if err != nil || len(rows) != 1 || rows[0]["n"].(float64) != 0 {
				t.Fatal(rows, err)
			}
		})
	}
}
func TestRequestedMemoryKinds(t *testing.T) {
	s := store(t)
	for _, kind := range []string{"preference", "correction", "infrastructure_fact", "device", "project", "constraint", "decision", "incident", "successful_action", "failed_approach", "recovery_outcome", "ai_handoff", "known_risk", "known_safe_pattern"} {
		if _, _, err := s.Write(context.Background(), Record{Kind: kind, Title: kind, Body: "fixture"}); err != nil {
			t.Fatal(kind, err)
		}
	}
}
