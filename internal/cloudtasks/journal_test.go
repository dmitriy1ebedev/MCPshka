package cloudtasks

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcpshka/internal/statestore"
)

func fixture(t *testing.T) *Journal {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func plan(id string) Plan {
	return Plan{ID: id, Provider: "codex_cloud", Repository: "dmitriy1ebedev/mcpshka", SourceDigest: strings.Repeat("a", 64), RequestID: "request-" + id}
}
func event(id string, rev uint64, status string) Observation {
	return Observation{EventID: id, ExpectedRevision: rev, ProviderTaskID: "provider-task-1", ProviderState: status, SourceDigest: strings.Repeat("a", 64)}
}
func TestCloudJournalStageRecoverAndReplay(t *testing.T) {
	j := fixture(t)
	p := plan("a")
	first, err := j.Stage(p)
	if err != nil || first.State != "planned" || first.Revision != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	again, err := j.Stage(p)
	if err != nil || again != first {
		t.Fatalf("nonidempotent stage %+v %v", again, err)
	}
	after, err := j.Observe("a", event("event-one", 1, "running"))
	if err != nil || after.State != "hold" || after.Revision != 2 {
		t.Fatalf("%+v %v", after, err)
	}
	duplicate, err := j.Observe("a", event("event-one", 1, "running"))
	if err != nil || duplicate != after {
		t.Fatalf("replay %+v %v", duplicate, err)
	}
	finished, err := j.Observe("a", event("event-two", 2, "completed"))
	if err != nil || finished.State != "observed_completed" || finished.DeployAuthorized || finished.DispatchEnabled || finished.Trust != "UNTRUSTED" {
		t.Fatalf("%+v %v", finished, err)
	}
	replayPlan, err := j.Stage(p)
	if err != nil || replayPlan != finished {
		t.Fatalf("plan replay after observation must read receipt: %+v %v", replayPlan, err)
	}
	recovered, err := j.Get("a")
	if err != nil || recovered != finished {
		t.Fatalf("recover %+v %v", recovered, err)
	}
	list, err := j.List(1)
	if err != nil || len(list) != 1 || list[0] != finished {
		t.Fatalf("list %+v %v", list, err)
	}
}
func TestCloudJournalRejectsStaleAndForeignAndTampered(t *testing.T) {
	j := fixture(t)
	_, _ = j.Stage(plan("a"))
	o := event("one", 1, "running")
	if _, err := j.Observe("a", Observation{EventID: o.EventID, ExpectedRevision: 1, ProviderTaskID: o.ProviderTaskID, ProviderState: "running", SourceDigest: strings.Repeat("b", 64)}); err == nil {
		t.Fatal("wrong source accepted")
	}
	first, err := j.Observe("a", o)
	if err != nil {
		t.Fatal(err)
	}
	if first.DeployAuthorized {
		t.Fatal("remote status authorizes deploy")
	}
	bad := o
	bad.ProviderState = "failed"
	if _, err = j.Observe("a", bad); err == nil {
		t.Fatal("reused event with different payload accepted")
	}
	if _, err = j.Observe("a", event("two", 1, "failed")); err == nil {
		t.Fatal("stale revision accepted")
	}
	wrong := event("two", 2, "failed")
	wrong.ProviderTaskID = "foreign-worker"
	if _, err = j.Observe("a", wrong); err == nil {
		t.Fatal("provider reassignment accepted")
	}
	next, err := j.Observe("a", event("two", 2, "failed"))
	if err != nil || next.State != "observed_failed" {
		t.Fatalf("%+v %v", next, err)
	}
	if _, err = j.Observe("a", event("three", 3, "success")); err == nil {
		t.Fatal("unknown status accepted")
	}
}
func TestCloudJournalInvalidPlansAndBounded(t *testing.T) {
	j := fixture(t)
	cases := []Plan{{}, {ID: "a", Provider: "evil", Repository: "owner/repo", SourceDigest: strings.Repeat("a", 64), RequestID: "req"}, {ID: "../a", Provider: "codex_cloud", Repository: "owner/repo", SourceDigest: strings.Repeat("a", 64), RequestID: "req"}, {ID: "a", Provider: "codex_cloud", Repository: "owner/../repo", SourceDigest: strings.Repeat("a", 64), RequestID: "req"}}
	for _, p := range cases {
		if _, err := j.Stage(p); err == nil {
			t.Fatalf("invalid plan %+v", p)
		}
	}
	p := plan("a")
	_, err := j.Stage(p)
	if err != nil {
		t.Fatal(err)
	}
	p.RequestID = "different"
	if _, err = j.Stage(p); err == nil {
		t.Fatal("changed plan accepted")
	}
	foreign := plan("another-task")
	foreign.RequestID = "request-a"
	if _, err = j.Stage(foreign); err == nil {
		t.Fatal("duplicate request ID across independent tasks accepted")
	}
	if _, err = j.List(21); err == nil {
		t.Fatal("oversized list")
	}
	for n := 0; n < 63; n++ {
		p := plan("id-" + strings.Repeat("x", n/10) + string(rune('a'+n%10))) // distinct IDs by suffix and repetition
		if _, err := j.Stage(p); err != nil {
			t.Fatalf("stage %d: %v", n, err)
		}
	}
	if _, err = j.Stage(plan("overflow")); err == nil {
		t.Fatal("capacity exceeded without HOLD")
	}
}
func TestCloudJournalReopenSQLiteAndRejectForeignEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Stage(plan("reopen")); err != nil {
		t.Fatal(err)
	}
	observed, err := writer.Observe("reopen", event("first", 1, "completed"))
	if err != nil {
		t.Fatal(err)
	}
	recoveredDB, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(recoveredDB)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restored.Get("reopen")
	if err != nil || got != observed || got.DeployAuthorized {
		t.Fatalf("SQLite restart lost or authorized task: %+v %v", got, err)
	}
	replay, err := restored.Observe("reopen", event("first", 1, "completed"))
	if err != nil || replay != observed {
		t.Fatalf("restart replay failed %+v %v", replay, err)
	}
	if _, err = restored.Observe("reopen", event("forged", 1, "completed")); err == nil {
		t.Fatal("stale event after restart accepted")
	}
}

func TestCloudJournalHistoricalEventIDsAndTerminalStateHold(t *testing.T) {
	j := fixture(t)
	if _, err := j.Stage(plan("history")); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		id       string
		revision uint64
		state    string
	}{
		{"one", 1, "running"}, {"two", 2, "in_progress"},
	} {
		if _, err := j.Observe("history", event(pair.id, pair.revision, pair.state)); err != nil {
			t.Fatal(err)
		}
	}
	old := event("one", 3, "running")
	if _, err := j.Observe("history", old); err == nil {
		t.Fatal("reused historical event ID with new revision accepted")
	}
	done, err := j.Observe("history", event("three", 3, "completed"))
	if err != nil || done.State != "observed_completed" {
		t.Fatalf("%+v %v", done, err)
	}
	replay, err := j.Observe("history", event("three", 3, "completed"))
	if err != nil || replay != done {
		t.Fatalf("terminal replay must be idempotent %+v %v", replay, err)
	}
	if _, err := j.Observe("history", event("four", 4, "running")); err == nil {
		t.Fatal("terminal task silently reopened")
	}
}

func TestCloudJournalConcurrentObservationSingleWriter(t *testing.T) {
	j := fixture(t)
	_, err := j.Stage(plan("a"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, s := range []string{"completed", "failed"} {
		wg.Add(1)
		go func(status string) {
			defer wg.Done()
			_, e := j.Observe("a", event("evt-"+status, 1, status))
			results <- e
		}(s)
	}
	wg.Wait()
	close(results)
	success, failed := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else {
			failed++
		}
	}
	if success != 1 || failed != 1 {
		t.Fatalf("concurrent CAS must choose one: successes=%d failures=%d", success, failed)
	}
	final, err := j.Get("a")
	if err != nil || final.Revision != 2 || final.DeployAuthorized {
		t.Fatalf("%+v %v", final, err)
	}
}

func TestCloudJournalInterruptedClaimPinsCompletePlan(t *testing.T) {
	for _, field := range []string{"provider", "repository"} {
		t.Run(field, func(t *testing.T) {
			j := fixture(t)
			original := plan("interrupted")
			if _, err := j.Stage(original); err != nil {
				t.Fatal(err)
			}
			claim, err := j.db.Get(requestPrefix + original.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate the durable request claim surviving an interrupted task write.
			recovery := fixture(t)
			if _, err := recovery.db.PutIfAbsent(requestPrefix+original.RequestID, claim); err != nil {
				t.Fatal(err)
			}
			changed := original
			if field == "provider" {
				changed.Provider = "copilot_cloud"
			} else {
				changed.Repository = "another/repo"
			}
			if _, err := recovery.Stage(changed); err == nil {
				t.Fatal("interrupted claim rebound to changed plan")
			}
			got, err := recovery.Stage(original)
			if err != nil || got.Plan != original {
				t.Fatalf("original recovery: %+v %v", got, err)
			}
			previous, _ := json.Marshal(got)
			got.Provider = "copilot_cloud"
			raw, _ := json.Marshal(got)
			if won, err := recovery.db.Replace(key(got.ID), previous, raw); err != nil || !won {
				t.Fatal(err)
			}
			if _, err := recovery.Get(got.ID); err == nil {
				t.Fatal("changed durable plan accepted with original request claim")
			}
		})
	}
}
