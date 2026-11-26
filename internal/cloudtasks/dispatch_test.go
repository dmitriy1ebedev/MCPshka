package cloudtasks

import (
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func dispatchPlan() DispatchRequest {
	return DispatchRequest{ID: DispatchID("owner", "once"), Owner: "owner", Provider: "codex_cloud", Commit: strings.Repeat("a", 40), Prompt: "Fix the bug", Acceptance: "Targeted tests pass"}
}

func TestDispatchClaimReplayConflictAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := New(db)
	p := dispatchPlan()
	first, won, err := j.Claim(p)
	if err != nil || !won || first.State != "unknown" {
		t.Fatalf("%+v %v %v", first, won, err)
	}
	db2, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j2, _ := New(db2)
	again, won, err := j2.Claim(p)
	if err != nil || won || again != first {
		t.Fatalf("restart must not dispatch: %+v %v %v", again, won, err)
	}
	p.Acceptance = "different"
	if _, _, err = j2.Claim(p); err == nil {
		t.Fatal("changed acceptance reused retry identity")
	}
	if strings.Contains(first.RequestSHA256, "Fix") || first.DeployAuthorized {
		t.Fatal("instructions or authority in receipt")
	}
}

func TestDispatchConcurrentClaimSingleSubmitter(t *testing.T) {
	j := fixture(t)
	var wg sync.WaitGroup
	wins := make(chan bool, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, won, err := j.Claim(dispatchPlan())
			if err != nil {
				t.Error(err)
			}
			wins <- won
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for won := range wins {
		if won {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("submitters=%d", n)
	}
}

func TestDispatchPinsCASAndResultBoundary(t *testing.T) {
	j := fixture(t)
	old, _, err := j.Claim(dispatchPlan())
	if err != nil {
		t.Fatal(err)
	}
	next := old
	next.State = "submitted"
	next.ProviderTaskID = "task_e_1"
	next.SnapshotCommit = strings.Repeat("b", 40)
	next.SourceTree = strings.Repeat("c", 40)
	submitted, err := j.Finish(old, next)
	if err != nil {
		t.Fatal(err)
	}
	invalid := submitted
	invalid.State = "forged"
	if _, err = j.Finish(submitted, invalid); err == nil {
		t.Fatal("invalid state poisoned journal")
	}
	if got, err := j.Dispatch(submitted.ID); err != nil || got != submitted {
		t.Fatal("invalid update changed receipt", err)
	}
	if _, err = j.Finish(old, next); err == nil {
		t.Fatal("stale writer replaced receipt")
	}
	next = submitted
	next.State = "result"
	next.ResultBranch = "main"
	next.ResultCommit = strings.Repeat("d", 40)
	next.PullRequest = "https://github.com/dmitriy1ebedev/mcpshka/pull/1"
	if _, err = j.Finish(submitted, next); err == nil {
		t.Fatal("main accepted as cloud result")
	}
	next.ResultBranch = "cloud-agents/" + next.ID
	result, err := j.Finish(submitted, next)
	if err != nil || result.DeployAuthorized {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err = j.Finish(result, submitted); err == nil {
		t.Fatal("terminal receipt reopened")
	}
	list, err := j.DispatchList(1)
	if err != nil || len(list) != 1 || list[0] != result {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestDispatchRequestBounds(t *testing.T) {
	for _, mutate := range []func(*DispatchRequest){
		func(p *DispatchRequest) { p.Prompt = "" }, func(p *DispatchRequest) { p.Provider = "local" },
		func(p *DispatchRequest) { p.Commit = "main" }, func(p *DispatchRequest) { p.Acceptance = "" },
		func(p *DispatchRequest) { p.Prompt = strings.Repeat("x", 2001) },
		func(p *DispatchRequest) { p.Prompt = strings.Repeat("\x01", 1000) },
	} {
		p := dispatchPlan()
		mutate(&p)
		if p.Validate() == nil {
			t.Fatalf("invalid request accepted %+v", p)
		}
	}
}
