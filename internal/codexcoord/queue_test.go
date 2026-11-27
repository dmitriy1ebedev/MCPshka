package codexcoord

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcpshka/internal/statestore"
)

func testQueue(t *testing.T) *Queue {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	q, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func testTask(id, model, effort string) Task {
	return Task{
		ID: id, Provider: "codex", Worktree: "/tmp/isolated-" + id,
		RoadmapSHA: strings.Repeat("a", 64), SessionID: "session_1234",
		Message: "Read-only investigation " + id, Model: model, Effort: effort,
	}
}

func TestThreeTasksHaveIndependentDurableModelSelections(t *testing.T) {
	q := testQueue(t)
	catalog := []Model{{ID: "model-fast", Efforts: []string{"low", "medium"}}, {ID: "model-deep", Efforts: []string{"medium", "high", "xhigh"}}}
	tasks := []Task{
		testTask("task-a", "model-fast", "low"),
		testTask("task-b", "model-deep", "high"),
		testTask("task-c", "model-deep", "xhigh"),
	}
	for _, task := range tasks {
		saved, err := q.Enqueue(task, catalog)
		if err != nil || saved.State != Queued || saved.Revision != 1 {
			t.Fatalf("enqueue: %#v %v", saved, err)
		}
		again, err := q.Enqueue(task, catalog)
		if err != nil || again != saved {
			t.Fatalf("idempotent enqueue: %#v %v", again, err)
		}
	}
	for _, task := range tasks {
		got, err := q.Get(task.ID)
		if err != nil || got.Model != task.Model || got.Effort != task.Effort {
			t.Fatalf("selection lost after durable read: %#v %v", got, err)
		}
	}
	pending, err := q.Pending(2)
	if err != nil || len(pending) != 2 || pending[0].ID != "task-a" || pending[1].ID != "task-b" {
		t.Fatalf("bounded pending order: %#v %v", pending, err)
	}
	if _, err := q.Pending(0); err == nil {
		t.Fatal("unbounded pending request accepted")
	}
	if _, err := q.Enqueue(testTask("task-a", "model-deep", "high"), catalog); err == nil {
		t.Fatal("conflicting duplicate task accepted")
	}
	updated, err := q.Reconfigure("task-a", 1, "model-deep", "medium", catalog)
	if err != nil || updated.Revision != 2 || updated.Model != "model-deep" {
		t.Fatalf("queued switch: %#v %v", updated, err)
	}
	if _, err := q.Reconfigure("task-a", 1, "model-deep", "high", catalog); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err := q.Reconfigure("task-a", 2, "model-fast", "xhigh", catalog); err == nil {
		t.Fatal("unsupported effort accepted")
	}
	held, err := q.Suspend("task-a", 2)
	if err != nil || held.State != Hold {
		t.Fatalf("HOLD: %#v %v", held, err)
	}
	if _, err := q.Reconfigure("task-a", 3, "model-deep", "high", catalog); err == nil {
		t.Fatal("HOLD was silently resumed")
	}
	for _, id := range []string{"task-b", "task-c"} {
		got, err := q.Get(id)
		if err != nil || got.State != Queued || got.Revision != 1 {
			t.Fatalf("other task changed: %#v %v", got, err)
		}
	}
}

func TestConcurrentReconfigureHasExactlyOneWinner(t *testing.T) {
	q := testQueue(t)
	catalog := []Model{{ID: "model-a", Efforts: []string{"low", "high"}}}
	if _, err := q.Enqueue(testTask("one", "model-a", "low"), catalog); err != nil {
		t.Fatal(err)
	}
	const attempts = 2
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = q.Reconfigure("one", 1, "model-a", "high", catalog) }(i)
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("CAS winners=%d, errors=%v", wins, errs)
	}
	got, err := q.Get("one")
	if err != nil || got.Revision != 2 || got.Effort != "high" {
		t.Fatalf("reconciled state: %#v %v", got, err)
	}
}

func TestInvalidTasksNeverPersist(t *testing.T) {
	q := testQueue(t)
	catalog := []Model{{ID: "model-a", Efforts: []string{"low"}}}
	for _, modify := range []func(*Task){
		func(task *Task) { task.ID = "../unsafe" },
		func(task *Task) { task.Provider = "other" },
		func(task *Task) { task.SessionID = "bad" },
		func(task *Task) { task.Worktree = "relative" },
		func(task *Task) { task.Worktree = "/" },
		func(task *Task) { task.Worktree = "/repo\x00hidden" },
		func(task *Task) { task.Worktree = "/repo\xff" },
		func(task *Task) { task.RoadmapSHA = "unknown" },
		func(task *Task) { task.Message = "" },
		func(task *Task) { task.Message = " \n " },
		func(task *Task) { task.Message = "hidden\x00action" },
		func(task *Task) { task.Message = "invalid\xff" },
		func(task *Task) { task.Model = "not-in-catalog" },
		func(task *Task) { task.Effort = "xhigh" },
		func(task *Task) { task.DispatchID = "fake-dispatch" },
		func(task *Task) { task.Revision = 99 },
		func(task *Task) { task.State = Reserved },
	} {
		task := testTask("unsafe", "model-a", "low")
		modify(&task)
		if _, err := q.Enqueue(task, catalog); err == nil {
			t.Fatalf("invalid task accepted: %#v", task)
		}
	}
	if _, err := q.Get("unsafe"); err == nil {
		t.Fatal("invalid task was persisted")
	}
}

func TestThreeIndependentReservationsSurviveRestartWithoutDispatch(t *testing.T) {
	database := filepath.Join(t.TempDir(), "state.db")
	db, err := statestore.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	q, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	catalog := []Model{{ID: "model-fast", Efforts: []string{"low", "medium"}}, {ID: "model-deep", Efforts: []string{"high", "xhigh"}}}
	tasks := []Task{
		testTask("task-one", "model-fast", "low"),
		testTask("task-two", "model-deep", "high"),
		testTask("task-three", "model-deep", "xhigh"),
	}
	for i, task := range tasks {
		task.SessionID = "session_unique_" + string(rune('a'+i))
		if _, err := q.Enqueue(task, catalog); err != nil {
			t.Fatal(err)
		}
		got, err := q.Reserve(task.ID, 1, "dispatch-"+task.ID)
		if err != nil || got.State != Reserved || got.Model != task.Model || got.Effort != task.Effort {
			t.Fatalf("task %d reservation failed: %+v %v", i, got, err)
		}
	}
	// A second DB connection represents a fresh AI context, without a Codex call.
	reopened, err := statestore.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(reopened)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		id := "dispatch-" + task.ID
		got, err := other.Reserve(task.ID, 1, id)
		if err != nil || got.Revision != 2 || got.State != Reserved {
			t.Fatalf("replay re-dispatched a task: %+v %v", got, err)
		}
		receipt, err := other.InspectReservation(task.ID)
		if err != nil || !receipt.ClaimsVerified || receipt.DispatchEnabled ||
			receipt.NextSafeAction != "owner_authorization_and_protocol_acceptance_required" {
			t.Fatalf("unsafe/false DONE receipt: %+v %v", receipt, err)
		}
		if _, err := other.Reserve(task.ID, 2, "different-"+id); err == nil {
			t.Fatal("new key bypassed durable reservation")
		}
	}
}

func TestReserveConflictsAndCrashRecoveryHold(t *testing.T) {
	q := testQueue(t)
	catalog := []Model{{ID: "model", Efforts: []string{"low", "high"}}}
	a := testTask("first", "model", "low")
	b := testTask("second", "model", "high")
	b.SessionID = a.SessionID // different worktree, same Codex session
	c := testTask("third", "model", "low")
	c.Worktree = a.Worktree // different task/session, same Git worktree
	c.SessionID = "session_new1234"
	for _, task := range []Task{a, b, c} {
		if _, err := q.Enqueue(task, catalog); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.Reserve(a.ID, 100, "wrong-revision"); err == nil {
		t.Fatal("stale revision gained a claim")
	}
	first, err := q.Reserve(a.ID, 1, "intent-first")
	if err != nil || first.State != Reserved {
		t.Fatalf("first reservation: %v", err)
	}
	if _, err := q.Reserve(b.ID, 1, "intent-second"); err == nil {
		t.Fatal("same session used by two tasks")
	}
	if _, err := q.Reserve(c.ID, 1, "intent-third"); err == nil {
		t.Fatal("same worktree used by two tasks")
	}
	// Simulate a crashed supervisor after a session claim but before task CAS.
	d := testTask("partial", "model", "high")
	d.SessionID = "session_orphan1234"
	if _, err := q.Enqueue(d, catalog); err != nil {
		t.Fatal(err)
	}
	if err := q.bindClaim("codexcoord/claims/session/"+d.SessionID, d.ID+"\n"+"orphan-intent"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Reserve(d.ID, 1, "new-intent"); err == nil {
		t.Fatal("crash replay stole unknown intent")
	}
	original, err := q.Reserve(d.ID, 1, "orphan-intent")
	if err != nil || original.State != Reserved {
		t.Fatalf("same pinned identity must reconcile: %v", err)
	}
	if _, err := q.Reconfigure(d.ID, 2, "model", "low", catalog); err == nil {
		t.Fatal("reserved task model silently changed during possible dispatch")
	}
}

func TestInspectPartialReservationAfterRestartRequiresReconciliation(t *testing.T) {
	for _, stage := range []string{"session", "worktree", "both", "foreign", "held"} {
		t.Run(stage, func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "state.db")
			db, err := statestore.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			q, err := New(db)
			if err != nil {
				t.Fatal(err)
			}
			task := testTask("crashed", "model", "low")
			if _, err := q.Enqueue(task, []Model{{ID: "model", Efforts: []string{"low"}}}); err != nil {
				t.Fatal(err)
			}
			// The claims commit before the task CAS; a lost supervisor leaves it queued.
			owner := task.ID + "\noriginal-dispatch"
			if stage == "foreign" {
				owner = "another-task\nforeign-dispatch"
			}
			claimPath := "codexcoord/claims/session/" + task.SessionID
			if stage == "worktree" {
				claimPath = worktreeClaim(task.Worktree)
			}
			if err := q.bindClaim(claimPath, owner); err != nil {
				t.Fatal(err)
			}
			if stage == "both" {
				if err := q.bindClaim(worktreeClaim(task.Worktree), owner); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "held" {
				if _, err := q.Suspend(task.ID, 1); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := statestore.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := New(reopened)
			if err != nil {
				t.Fatal(err)
			}
			before, err := recovered.Get(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := recovered.InspectReservation(task.ID)
			if err != nil || receipt.NextSafeAction != "reconcile_dispatch_claims_without_replay" || receipt.ClaimsVerified || receipt.DispatchEnabled {
				t.Fatalf("orphan claims hidden after restart: %+v %v", receipt, err)
			}
			after, err := recovered.Get(task.ID)
			if err != nil || before != after {
				t.Fatalf("inspection mutated durable task: %+v %v", after, err)
			}
			actual, err := reopened.Get(claimPath)
			if err != nil || string(actual) != owner {
				t.Fatalf("inspection changed claim: %q %v", actual, err)
			}
			if _, err := recovered.Reserve(task.ID, before.Revision, "replacement-dispatch"); err == nil {
				t.Fatal("restart replay replaced unresolved dispatch identity")
			}
		})
	}
}

func TestInspectUnreservedTaskDoesNotInventRecovery(t *testing.T) {
	q := testQueue(t)
	task := testTask("unreserved", "model", "low")
	if _, err := q.Enqueue(task, []Model{{ID: "model", Efforts: []string{"low"}}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := q.InspectReservation(task.ID)
	if err != nil || receipt.NextSafeAction != "read_only_reconcile" || receipt.ClaimsVerified || receipt.DispatchEnabled {
		t.Fatalf("false reservation evidence: %+v %v", receipt, err)
	}
}
