package projectdrift

import (
	"testing"
	"time"
)

func TestDriftClaimsAreNotProductionAuthority(t *testing.T) {
	raw := []byte("## Журнал\n### Релизы\n| Версия | Дата | Статус |\n| 0.9.3 | 2026-11-08 | в проде |\n| 0.9.4 | 2026-11-08 | **в проде** |\n| 0.12.9 | 2026-11-09 | candidate |\n| 0.12.8 | 2026-11-09 | был в проде |\n### Очередь\n| 0.99.9 | 2026-11-10 | в проде |")
	if got := LastProduction(raw); got != "0.9.4" {
		t.Fatal(got)
	}
	now := time.Now().UTC()
	flags := Evaluate(raw, "0.12.6", "0.12.5", "0.12.6", "0.12.8", now)
	for _, f := range flags {
		if f.State != "DRIFT" || f.Mismatch == nil || !*f.Mismatch || f.Text == "" || f.Source == "" || !f.ObservedAt.Equal(now) {
			t.Fatal(f)
		}
	}
	flags = Evaluate(nil, "", "", "0.12.6", "", now)
	for _, f := range flags {
		if f.State != "UNKNOWN" || f.Mismatch != nil {
			t.Fatal(f)
		}
	}
	flags = Evaluate(raw, "0.9.4", "0.9.4", "0.9.4", "0.9.4", now)
	for _, f := range flags {
		if f.State != "MATCH" || f.Mismatch == nil || *f.Mismatch {
			t.Fatal(f)
		}
	}
}
