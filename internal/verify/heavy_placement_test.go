package verify

import (
	"context"
	"testing"
)

func TestHeavyPlacementReportsAnUnsplitServer(t *testing.T) {
	old := HeavyPlacement
	t.Cleanup(func() { HeavyPlacement = old })
	HeavyPlacement = func() (bool, string) { return false, "heavy jobs share the server cgroup" }
	s := &Service{run: func(context.Context, string, ...string) ([]byte, error) { return []byte("CPUWeight=1000\nIOWeight=1000\n"), nil }}
	var r Result
	s.addHomePriority(context.Background(), &r)
	for _, c := range r.Checks {
		if c.Name == "heavy_jobs_isolated" {
			if c.OK {
				t.Fatal("an unsplit server passed")
			}
			return
		}
	}
	t.Fatalf("no heavy_jobs_isolated check: %+v", r.Checks)
}
