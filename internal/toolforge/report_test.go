package toolforge

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSummarizeCountsCatalogExactly(t *testing.T) {
	tools := []Tool{
		{Name: "fs_read", ReadOnly: true, HasReady: true, Calls: -1},
		{Name: "fs_write", HasContract: true, HasReady: true, Calls: 3},
		{Name: "old_write", HasContract: true, Calls: 0},
		{Name: "codex_x", Calls: -1},
	}
	now := time.Now()
	reqs := []Request{{ID: "a", Goal: "older", State: "open", At: now.Add(-time.Hour)}, {ID: "b", Goal: "done", State: "released", At: now}, {ID: "c", Goal: "newer", State: "open", At: now}}
	got := Summarize(tools,
		func(n string) bool { return strings.HasPrefix(n, "codex_") || n == "old_write" },
		func(n string) string {
			if n == "fs_read" {
				return "ready"
			}
			return "unknown"
		},
		map[string]string{"old_write": "fs_write"}, reqs)
	want := Report{Registered: 4, Listed: 2, Hidden: 2, ReadOnly: 1, Mutating: 3, MutatingWithContract: 2, WithReadiness: 2,
		Readiness: map[string]int{"ready": 1, "unknown": 3}, Deprecated: map[string]string{"old_write": "fs_write"},
		UnusedMutations: []string{"old_write"}, MissingCapabilities: []Request{reqs[2], reqs[0]}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("report:\n%+v", got)
	}
}

func TestRequestsListsRecorded(t *testing.T) {
	old := Dir
	Dir = t.TempDir()
	t.Cleanup(func() { Dir = old })
	if len(Requests()) != 0 {
		t.Fatal("requests in an empty dir")
	}
	q := Request{ID: "0123456789abcdef", Goal: "read router logs", Missing: "no router tool", Acceptance: "logs come back", State: "open"}
	if err := SaveRequest(q); err != nil {
		t.Fatal(err)
	}
	if got := Requests(); len(got) != 1 || got[0].Goal != q.Goal {
		t.Fatalf("requests: %+v", got)
	}
}
