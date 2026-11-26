package codexcoord

import (
	"encoding/json"
	"fmt"
	"math"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestIntentCapacityIsAtomicAcrossConcurrentCreators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capacity.db")
	db, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := New(db)
	otherDB, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := New(otherDB)
	fixture := Intent{SessionID: "session_capacity_123", Worktree: "/tmp/capacity", RoadmapSHA: strings.Repeat("a", 64), Message: "capacity fixture", Model: "model", Effort: "low"}
	for n := 0; n < 127; n++ {
		fixture.ID = fmt.Sprintf("seed-%03d", n)
		if _, err := q.StageIntent(fixture); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for n := 0; n < 2; n++ {
		i := fixture
		i.ID = fmt.Sprintf("contender-%d", n)
		writer := q
		if n == 1 {
			writer = other
		}
		go func() { <-start; _, err := writer.StageIntent(i); results <- err }()
	}
	close(start)
	wins := 0
	for n := 0; n < 2; n++ {
		if <-results == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("capacity winners=%d, want one", wins)
	}
	if _, err := q.ListIntents(50); err != nil {
		t.Fatal("capacity race made durable state unreadable", err)
	}
	if _, err := q.StageIntent(fixture); err != nil {
		t.Fatal("full capacity must preserve existing identity replay", err)
	}
}

func TestIntentRevisionExhaustionPreservesReceipt(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	q, _ := New(db)
	i := Intent{ID: "exhausted", SessionID: "session_exhausted", Worktree: "/tmp/isolated-intent", RoadmapSHA: strings.Repeat("a", 64), Message: "isolated", Model: "model", Effort: "high", State: "draft", Revision: math.MaxUint64}
	raw, _ := json.Marshal(i)
	if ok, err := db.PutIfAbsent(intentKey(i.ID), raw); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := q.ChangeIntent(i.ID, i.Revision, "other", "high"); err == nil {
		t.Fatal("wrapped revision accepted")
	}
	if _, err := q.CancelIntent(i.ID, i.Revision); err == nil {
		t.Fatal("wrapped cancel accepted")
	}
	got, err := q.ReadIntent(i.ID)
	if err != nil || got != i {
		t.Fatal("rejected mutation damaged receipt", err)
	}
}

func TestIntentThreeTaskRecoveryAndModelSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intents.db")
	db, e := statestore.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	q, _ := New(db)
	for _, spec := range []struct{ id, model, effort string }{{"one", "quick", "low"}, {"two", "deep", "high"}, {"three", "deep", "xhigh"}} {
		i := Intent{ID: spec.id, SessionID: "session_" + spec.id + "abcd", Worktree: "/tmp/r3-" + spec.id, RoadmapSHA: strings.Repeat("a", 64), Message: "review " + spec.id, Model: spec.model, Effort: spec.effort}
		v, e := q.StageIntent(i)
		if e != nil || v.DispatchEnabled || v.CatalogVerified || v.State != "draft" {
			t.Fatalf("%+v %v", v, e)
		}
		again, e := q.StageIntent(i)
		if e != nil || again != v {
			t.Fatalf("repeat %+v %v", again, e)
		}
	}
	restarted, e := statestore.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	fresh, _ := New(restarted)
	list, e := fresh.ListIntents(10)
	if e != nil || len(list) != 3 {
		t.Fatalf("reopen %+v %v", list, e)
	}
	revised, e := fresh.ChangeIntent("one", 1, "deep", "medium")
	if e != nil || revised.Revision != 2 || revised.DispatchEnabled || revised.CatalogVerified {
		t.Fatalf("reconfigure %+v %v", revised, e)
	}
	if _, e = fresh.ChangeIntent("one", 1, "wrong", "high"); e == nil {
		t.Fatal("stale CAS permitted")
	}
	canceled, e := fresh.CancelIntent("one", 2)
	if e != nil || canceled.State != "cancelled" {
		t.Fatal(canceled, e)
	}
	if _, e = fresh.ChangeIntent("one", 3, "quick", "low"); e == nil {
		t.Fatal("cancelled intent edited")
	}
	if _, e = fresh.StageIntent(Intent{ID: "one"}); e == nil {
		t.Fatal("conflict accepted")
	}
}
func TestIntentCASAndNoExecutableAuthority(t *testing.T) {
	q := testQueue(t)
	i := Intent{ID: "race", SessionID: "session_race123", Worktree: "/tmp/race", RoadmapSHA: strings.Repeat("b", 64), Message: "do not launch Codex", Model: "quick", Effort: "low"}
	if _, e := q.StageIntent(i); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for k := range results {
		wg.Add(1)
		go func(n int) { defer wg.Done(); _, results[n] = q.ChangeIntent("race", 1, "deep", "high") }(k)
	}
	wg.Wait()
	winners := 0
	for _, e := range results {
		if e == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("CAS succeeded %d times", winners)
	}
	v, e := q.ReadIntent("race")
	if e != nil || v.Revision != 2 || v.State != "draft" {
		t.Fatal(v, e)
	}
	result := summarizeIntent(v)
	if result.DispatchEnabled || result.CatalogVerified {
		t.Fatal("fabricated verification")
	}
}
func TestIntentRejectUnsafeInputs(t *testing.T) {
	q := testQueue(t)
	good := Intent{ID: "good", SessionID: "session_good123", Worktree: "/tmp/good", RoadmapSHA: strings.Repeat("c", 64), Message: "safe", Model: "model", Effort: "low"}
	for _, bad := range []func(*Intent){func(i *Intent) { i.ID = "../bad" }, func(i *Intent) { i.Worktree = "/" }, func(i *Intent) { i.Model = "../../unsafe" }, func(i *Intent) { i.Message = "" }, func(i *Intent) { i.Effort = "turbo" }, func(i *Intent) { i.Revision = 9 }, func(i *Intent) { i.State = "running" }} {
		i := good
		bad(&i)
		if _, e := q.StageIntent(i); e == nil {
			t.Fatalf("accepted %+v", i)
		}
	}
	if items, e := q.ListIntents(50); e != nil || len(items) != 0 {
		t.Fatal(items, e)
	}
}
