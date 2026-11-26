package issues

import (
	"path/filepath"
	"testing"
)

func TestIncidentHistoryOnlyRaisesRisk(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "issues.json"))
	a, err := s.Create(NewIssue{Title: "Worker 123 timed out", Priority: "P0", Project: "app", Category: "worker", Body: "timeout"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(a.ID, Patch{Priority: "P3", Note: "lower current urgency"}, "test"); err != nil {
		t.Fatal(err)
	}
	incoming := Issue{Title: "Worker 456 timed out", Priority: "P3", Project: "app", Category: "worker"}
	got := s.IncidentFor(incoming)
	if got.Fingerprint == "" || got.RiskFloor != "P0" || len(got.SimilarIncidents) != 1 || got.SimilarIncidents[0].ID != a.ID || len(got.SuggestedActions) != 1 {
		t.Fatal(got)
	}
	incoming.Priority = "P0"
	if got = s.IncidentFor(incoming); got.RiskFloor != "P0" {
		t.Fatal(got)
	}
	incoming.Project = "other"
	if got = s.IncidentFor(incoming); len(got.SimilarIncidents) != 0 {
		t.Fatal(got)
	}
}
