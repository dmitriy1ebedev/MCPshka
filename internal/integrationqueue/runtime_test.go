package integrationqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func runtimeFixture(t *testing.T) (*Runtime, Request, string) {
	t.Helper()
	root, state := t.TempDir(), t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	gitTest(t, root, "config", "user.name", "Integration Test")
	gitTest(t, root, "config", "user.email", "integration@example.invalid")
	roadmap := []byte("## P7 — Integration\n- [ ] durable queue\n")
	if err := os.WriteFile(filepath.Join(root, "ROADMAP.md"), roadmap, 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "add", "ROADMAP.md")
	gitTest(t, root, "commit", "-m", "base")
	base := gitTest(t, root, "rev-parse", "HEAD")
	gitTest(t, root, "checkout", "-b", "feature")
	gitTest(t, root, "commit", "--allow-empty", "-m", "candidate")
	commit := gitTest(t, root, "rev-parse", "HEAD")
	file := filepath.Join(state, "state.db")
	db, err := statestore.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(roadmap)
	request := Request{Owner: "test-controller", Path: root, ID: "request-1", Branch: "feature", Commit: commit, TargetBranch: "main", TargetHead: base, RoadmapSHA: hex.EncodeToString(sum[:]), Scope: "P7"}
	return NewRuntime(db, &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}), request, file
}

func TestRuntimeEnqueueNamesInvalidPin(t *testing.T) {
	r, request, _ := runtimeFixture(t)
	request.Commit = request.Commit[:7]
	if _, err := r.Enqueue(context.Background(), request); err == nil || !strings.Contains(err.Error(), "commit (full 40-hex SHA required)") {
		t.Fatalf("short commit error = %v", err)
	}
}

func TestRuntimeColdRetryAndCancellation(t *testing.T) {
	s, request, file := runtimeFixture(t)
	ctx := context.Background()
	first, err := s.Enqueue(ctx, request)
	if err != nil || !first.Found || first.Item.State != Pending || first.Item.ExecutionID == "" {
		t.Fatalf("enqueue: %+v %v", first, err)
	}
	// The Git branch may advance after admission; retry returns the durable pin.
	gitTest(t, request.Path, "commit", "--allow-empty", "-m", "later work")
	db, err := statestore.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	cold := NewRuntime(db, s.policy)
	retry, err := cold.Enqueue(ctx, request)
	if err != nil || retry.Item.ExecutionID != first.Item.ExecutionID {
		t.Fatalf("cold retry: %+v %v", retry, err)
	}
	conflict := request
	conflict.Commit = gitTest(t, request.Path, "rev-parse", "HEAD")
	if _, err := cold.Enqueue(ctx, conflict); err == nil {
		t.Fatal("idempotency conflict accepted")
	}
	if _, err := cold.CancelAs(ctx, request.Path, request.ID, conflict.Commit, request.Owner); err == nil {
		t.Fatal("wrong commit cancelled request")
	}
	cancelled, err := cold.CancelAs(ctx, request.Path, request.ID, request.Commit, request.Owner)
	if err != nil || cancelled.Item.State != Cancelled {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	again, err := s.CancelAs(ctx, request.Path, request.ID, request.Commit, request.Owner)
	if err != nil || again.Item.ExecutionID != first.Item.ExecutionID {
		t.Fatalf("cancel retry: %+v %v", again, err)
	}
	if head := gitTest(t, request.Path, "rev-parse", "main"); head != request.TargetHead {
		t.Fatal("queue mutated target Git ref")
	}
}

func TestRuntimeConcurrentAdmissionBoundAndLinkedWorktree(t *testing.T) {
	s, request, file := runtimeFixture(t)
	s.capacity = 3
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, request.Path, "worktree", "add", "--detach", linked, request.Commit)
	p := &policy.Policy{AllowedWorkdirs: []string{request.Path, linked}, DeniedCommands: map[string]struct{}{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := statestore.Open(file)
			if err != nil {
				t.Error(err)
				return
			}
			other := NewRuntime(db, p)
			other.capacity = s.capacity
			r := request
			r.ID = fmt.Sprintf("parallel-%d", i)
			if i%2 == 0 {
				r.Path = linked
			}
			if _, err := other.Enqueue(context.Background(), r); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	status, err := s.Status(context.Background(), request.Path, "")
	if err != nil || accepted != 3 || status.Count != 3 {
		t.Fatalf("atomic capacity: accepted=%d status=%+v err=%v", accepted, status, err)
	}
}

func TestRuntimeForeignOwnerCannotCancel(t *testing.T) {
	s, request, _ := runtimeFixture(t)
	if _, err := s.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelAs(context.Background(), request.Path, request.ID, request.Commit, "another-controller"); err == nil {
		t.Fatal("foreign owner cancelled request")
	}
}

func TestRuntimeRejectsChangedPinsAndDamagedAuthority(t *testing.T) {
	s, request, _ := runtimeFixture(t)
	cases := []Request{request, request, request}
	cases[0].Commit = request.TargetHead
	cases[1].TargetHead = request.Commit
	cases[2].RoadmapSHA = request.Commit
	for _, r := range cases {
		if _, err := s.Enqueue(context.Background(), r); err == nil {
			t.Fatal("invalid pin admitted")
		}
	}
	if _, err := s.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_, key, err := s.queue(context.Background(), request.Path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.db.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Replace(key, before, []byte(`{"version":1,"items":[{"state":"invented"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Status(context.Background(), request.Path, request.ID); err == nil {
		t.Fatal("damaged snapshot accepted")
	}
	if _, err := s.CancelAs(context.Background(), request.Path, request.ID, request.Commit, request.Owner); err == nil {
		t.Fatal("damaged snapshot overwritten")
	}
}

func TestRuntimeExpiredContextCannotAdmitOrCancel(t *testing.T) {
	s, request, _ := runtimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Enqueue(ctx, request); err == nil {
		t.Fatal("expired admission accepted")
	}
	status, err := s.Status(context.Background(), request.Path, request.ID)
	if err != nil || status.Count != 0 {
		t.Fatalf("expired admission mutated queue: %+v %v", status, err)
	}
	if _, err := s.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelAs(ctx, request.Path, request.ID, request.Commit, request.Owner); err == nil {
		t.Fatal("expired cancellation accepted")
	}
	status, err = s.Status(context.Background(), request.Path, request.ID)
	if err != nil || status.Item.State != Pending {
		t.Fatalf("expired cancellation mutated queue: %+v %v", status, err)
	}
}

func TestMarkPublishedClosesOnlyThePublishedItem(t *testing.T) {
	s, request, _ := runtimeFixture(t)
	ctx := context.Background()
	status, err := s.Enqueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	execution := status.Item.ExecutionID
	// Not yet published: the target still points at the admitted head.
	if err := s.MarkPublished(ctx, request, execution); err == nil {
		t.Fatal("item closed before the target moved")
	}
	gitTest(t, request.Path, "checkout", "-q", "main")
	gitTest(t, request.Path, "merge", "--ff-only", "-q", request.Commit)
	if err := s.MarkPublished(ctx, request, strings.Repeat("0", 32)); err == nil {
		t.Fatal("foreign execution closed the item")
	}
	if err := s.MarkPublished(ctx, request, execution); err != nil {
		t.Fatal(err)
	}
	after, err := s.Status(ctx, request.Path, request.ID)
	if err != nil || after.Item == nil || after.Item.State != Integrated {
		t.Fatalf("item not integrated: %+v %v", after, err)
	}
	if err := s.MarkPublished(ctx, request, execution); err != nil {
		t.Fatal("repeat must be a no-op", err)
	}
}
