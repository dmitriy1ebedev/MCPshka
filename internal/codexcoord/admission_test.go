package codexcoord

import (
	"reflect"
	"strings"
	"testing"
)

func admissionCase(id, session, tree, model, effort string) AdmissionRequest {
	t := Task{ID: id, Provider: "codex", SessionID: session, Worktree: tree,
		RoadmapSHA: strings.Repeat("a", 64), Message: "offline read-only review " + id,
		Model: model, Effort: effort, State: Queued, Revision: 1}
	f := ScopeFacts{SessionID: session, Worktree: tree, RoadmapSHA: t.RoadmapSHA,
		CatalogVerified: true, OwnerVerified: true, SessionIdle: true,
		WriterLeaseFree: true, ControlPathHealthy: true, QuotaVerified: true, QuotaAvailable: true}
	return AdmissionRequest{Task: t, Facts: f, Budget: ResourceBudget{WallSeconds: 60, CPUPercent: 50, MemoryMiB: 128, OutputKiB: 16}}
}
func testCatalog() []Model {
	return []Model{{ID: "model-fast", Efforts: []string{"low", "medium"}},
		{ID: "model-deep", Efforts: []string{"high", "xhigh"}}}
}
func TestAdmissionThreeIndependentScopedTasksNeverDispatch(t *testing.T) {
	req := []AdmissionRequest{
		admissionCase("gamma", "session_gamma123", "/tmp/gamma", "model-deep", "xhigh"),
		admissionCase("alpha", "session_alpha123", "/tmp/alpha", "model-fast", "low"),
		admissionCase("beta", "session_beta123", "/tmp/beta", "model-deep", "high"),
	}
	a, e := AssessBatch(req, testCatalog(), 3)
	if e != nil || len(a) != 3 {
		t.Fatalf("%+v %v", a, e)
	}
	for i, id := range []string{"alpha", "beta", "gamma"} {
		if a[i].TaskID != id || a[i].Status != "READY_FOR_APPROVAL" ||
			a[i].DispatchEnabled || a[i].IntentSHA256 == "" {
			t.Fatalf("false dispatch receipt for %s: %+v", id, a[i])
		}
	}
	b, e := AssessBatch(req, testCatalog(), 3)
	if e != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("replay changed offline decisions")
	}
}
func TestAdmissionBusyOrUnknownDoesNotBlockOthers(t *testing.T) {
	req := []AdmissionRequest{
		admissionCase("a", "session_busy123", "/tmp/a", "model-fast", "low"),
		admissionCase("b", "session_open123", "/tmp/b", "model-fast", "low"),
		admissionCase("c", "session_other123", "/tmp/c", "model-deep", "high"),
	}
	req[0].Facts.SessionIdle = false
	req[2].Facts.QuotaVerified = false
	rows, e := AssessBatch(req, testCatalog(), 3)
	if e != nil {
		t.Fatal(e)
	}
	if rows[0].Status != "HOLD" || rows[1].Status != "READY_FOR_APPROVAL" || rows[2].Status != "HOLD" {
		t.Fatalf("busy first task starved independent task: %+v", rows)
	}
	if !reflect.DeepEqual(rows[2].Reasons, []string{"quota_unknown"}) {
		t.Fatal(rows[2])
	}
}
func TestAdmissionConflictsAndBudgets(t *testing.T) {
	a := admissionCase("a", "session_same123", "/tmp/a", "model-fast", "low")
	b := admissionCase("b", "session_same123", "/tmp/b", "model-fast", "low")
	c := admissionCase("c", "session_other123", "/tmp/a", "model-deep", "high")
	rows, e := AssessBatch([]AdmissionRequest{c, b, a}, testCatalog(), 3)
	if e != nil || rows[0].Status != "READY_FOR_APPROVAL" ||
		!reflect.DeepEqual(rows[1].Reasons, []string{"session_already_selected"}) ||
		!reflect.DeepEqual(rows[2].Reasons, []string{"worktree_already_selected"}) {
		t.Fatalf("%+v %v", rows, e)
	}
	rows, e = AssessBatch([]AdmissionRequest{a, b, c}, testCatalog(), 1)
	if e != nil || rows[0].Status != "READY_FOR_APPROVAL" || rows[1].Status != "HOLD" || rows[2].Status != "HOLD" {
		t.Fatal(rows, e)
	}
	if _, e = AssessBatch(nil, testCatalog(), 33); e == nil {
		t.Fatal("unbounded capacity accepted")
	}
	if _, e = AssessBatch(make([]AdmissionRequest, 33), testCatalog(), 2); e == nil {
		t.Fatal("unbounded scan accepted")
	}
}
func TestAdmissionInvalidEvidenceNeverGrantsAuthority(t *testing.T) {
	a := admissionCase("a", "session_valid123", "/tmp/a", "model-fast", "low")
	for _, mutate := range []func(*AdmissionRequest){
		func(r *AdmissionRequest) { r.Facts.CatalogVerified = false },
		func(r *AdmissionRequest) { r.Facts.OwnerVerified = false },
		func(r *AdmissionRequest) { r.Facts.ControlPathHealthy = false },
		func(r *AdmissionRequest) { r.Facts.WriterLeaseFree = false },
		func(r *AdmissionRequest) { r.Facts.Worktree = "/tmp/other" },
		func(r *AdmissionRequest) { r.Task.Effort = "xhigh" },
		func(r *AdmissionRequest) { r.Task.State = Reserved },
		func(r *AdmissionRequest) { r.Task.Revision = 0 },
		func(r *AdmissionRequest) { r.Budget.MemoryMiB = 9999 },
		func(r *AdmissionRequest) { r.Facts.QuotaAvailable = false },
	} {
		copy := a
		mutate(&copy)
		got := Assess(copy, testCatalog())
		if got.Status != "HOLD" || got.DispatchEnabled || len(got.Reasons) == 0 {
			t.Fatalf("unsafe admission result: %+v", got)
		}
	}
	dup := AssessBatch
	rows, e := dup([]AdmissionRequest{a, a}, testCatalog(), 2)
	if e != nil || rows[1].Status != "HOLD" || rows[1].DispatchEnabled {
		t.Fatal(rows, e)
	}
}

// A persisted revision is not a new Enqueue input. Admission must accept
// genuine persisted queued tasks even when Enqueue validators reject an
// externally claimed state or revision.
func TestAdmissionOfRealDurableQueuedReceipt(t *testing.T) {
	q := testQueue(t)
	r := admissionCase("durable", "session_real1234", "/tmp/durable", "model-deep", "high")
	pinned := r.Task
	pinned.State, pinned.Revision = "", 0
	got, err := q.Enqueue(pinned, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	r.Task = got
	decision := Assess(r, testCatalog())
	if decision.Status != "READY_FOR_APPROVAL" || decision.DispatchEnabled {
		t.Fatalf("durable queued receipt incorrectly held: %+v", decision)
	}
	r.Task.Worktree = "/"
	decision = Assess(r, testCatalog())
	if decision.Status != "HOLD" || decision.DispatchEnabled {
		t.Fatalf("unsafe worktree was admitted: %+v", decision)
	}
}
