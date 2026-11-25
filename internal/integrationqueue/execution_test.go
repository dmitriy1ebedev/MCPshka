package integrationqueue

import (
	"context"
	"errors"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionRequiresControllerAuthority(t *testing.T) {
	s, r, _ := runtimeFixture(t)
	if _, err := s.Enqueue(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(context.Background(), r.Path, r.ID); err == nil {
		t.Fatal("caller admission became authority")
	}
	if gitTest(t, r.Path, "rev-parse", "main") != r.TargetHead {
		t.Fatal("unauthorized effect")
	}
}

func TestExecutionUsesPinnedSourceAcrossLinkedWorktrees(t *testing.T) {
	s, r, _ := runtimeFixture(t)
	if _, err := s.Enqueue(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, r.Path, "worktree", "add", "--detach", linked, r.Commit)
	s.policy.AllowedWorkdirs = append(s.policy.AllowedWorkdirs, linked)
	if err := os.WriteFile(filepath.Join(r.Path, "foreign-dirty"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	s.withAuthority = func(ctx context.Context, item RuntimeItem, effect func() error) error { return effect() }
	if _, err := s.Execute(context.Background(), linked, r.ID); err == nil {
		t.Fatal("clean caller bypassed dirty pinned source")
	}
	if gitTest(t, r.Path, "rev-parse", "main") != r.TargetHead {
		t.Fatal("dirty source integration mutated target")
	}
}

func TestExecutionDurableIntentPrecedesGitAndColdRecovery(t *testing.T) {
	s, r, file := runtimeFixture(t)
	if _, err := s.Enqueue(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.withAuthority = func(ctx context.Context, item RuntimeItem, effect func() error) error { return effect() }
	s.beforeGit = func() error {
		seen, err := s.Status(context.Background(), r.Path, r.ID)
		if err != nil || seen.Item.State != Integrating {
			t.Fatalf("intent missing: %+v %v", seen, err)
		}
		gitTest(t, r.Path, "update-ref", "refs/heads/main", r.Commit, r.TargetHead)
		return errors.New("simulated crash after Git effect")
	}
	if _, err := s.Execute(context.Background(), r.Path, r.ID); err == nil {
		t.Fatal("crash not simulated")
	}
	db, err := statestore.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	cold := NewRuntime(db, s.policy)
	cold.withAuthority = s.withAuthority
	cold.beforeGit = func() error { t.Fatal("completed Git mutation repeated"); return nil }
	seen, err := cold.Execute(context.Background(), r.Path, r.ID)
	if err != nil || seen.Item.State != Integrated {
		t.Fatalf("cold recovery: %+v %v", seen, err)
	}
	if _, err := cold.CancelAs(context.Background(), r.Path, r.ID, r.Commit, r.Owner); err == nil {
		t.Fatal("completed effect cancelled")
	}
}

func TestExecutionLostAuthorityKeepsIntentAndNoEffect(t *testing.T) {
	s, r, _ := runtimeFixture(t)
	if _, err := s.Enqueue(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.withAuthority = func(ctx context.Context, item RuntimeItem, effect func() error) error {
		return errors.New("expired fence")
	}
	if _, err := s.Execute(context.Background(), r.Path, r.ID); err == nil {
		t.Fatal("expired fence accepted")
	}
	seen, err := s.Status(context.Background(), r.Path, r.ID)
	if err != nil || seen.Item.State != Pending {
		t.Fatalf("unauthorized admission changed: %+v %v", seen, err)
	}
	if gitTest(t, r.Path, "rev-parse", "main") != r.TargetHead {
		t.Fatal("unauthorized update")
	}
}

func TestExecutionAuthorityCallbackMustRunExactlyOnce(t *testing.T) {
	for _, twice := range []bool{false, true} {
		t.Run(map[bool]string{false: "skip", true: "repeat"}[twice], func(t *testing.T) {
			s, r, _ := runtimeFixture(t)
			if _, err := s.Enqueue(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			s.withAuthority = func(ctx context.Context, item RuntimeItem, effect func() error) error {
				if twice {
					if err := effect(); err != nil {
						return err
					}
					return effect()
				}
				return nil
			}
			if _, err := s.Execute(context.Background(), r.Path, r.ID); err == nil {
				t.Fatal("broken fence contract accepted")
			}
		})
	}
}

func TestExecutionColdRecoveryRejectsSymbolicTarget(t *testing.T) {
	s, r, file := runtimeFixture(t)
	if _, err := s.Enqueue(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.withAuthority = func(ctx context.Context, item RuntimeItem, effect func() error) error { return effect() }
	s.beforeGit = func() error {
		gitTest(t, r.Path, "symbolic-ref", "refs/heads/main", "refs/heads/feature")
		return errors.New("crash")
	}
	if _, err := s.Execute(context.Background(), r.Path, r.ID); err == nil {
		t.Fatal("expected crash")
	}
	db, err := statestore.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	cold := NewRuntime(db, s.policy)
	cold.withAuthority = s.withAuthority
	if _, err := cold.Execute(context.Background(), r.Path, r.ID); err == nil {
		t.Fatal("symbolic target mistaken for completed mutation")
	}
	seen, err := cold.Status(context.Background(), r.Path, r.ID)
	if err != nil || seen.Item.State != Integrating {
		t.Fatalf("unknown reservation lost: %+v %v", seen, err)
	}
}

func TestExecutionColdRecoveryRejectsConflictingEffectReceipt(t *testing.T) {
	s, r, _ := runtimeFixture(t)
	ctx := context.Background()
	admitted, err := s.Enqueue(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	s.withAuthority = func(ctx context.Context, item RuntimeItem, effect func() error) error { return effect() }
	s.beforeGit = func() error {
		gitTest(t, r.Path, "update-ref", "refs/heads/main", r.Commit, r.TargetHead)
		gitTest(t, r.Path, "update-ref", "refs/mcpshka/integration/"+runtimeDigest(admitted.Item.ExecutionID), r.TargetHead)
		return errors.New("crash")
	}
	if _, err := s.Execute(ctx, r.Path, r.ID); err == nil {
		t.Fatal("crash missing")
	}
	s.beforeGit = nil
	if _, err := s.Execute(ctx, r.Path, r.ID); err == nil {
		t.Fatal("conflicting receipt ignored during exact-SHA recovery")
	}
}

func TestExecutionHappyPathPreservesConcurrentAdmissions(t *testing.T) {
	s, r, _ := runtimeFixture(t)
	ctx := context.Background()
	if _, err := s.Enqueue(ctx, r); err != nil {
		t.Fatal(err)
	}
	held := false
	s.withAuthority = func(ctx context.Context, item RuntimeItem, effect func() error) error {
		held = true
		defer func() { held = false }()
		return effect()
	}
	s.beforeGit = func() error {
		if !held {
			t.Fatal("effect escaped authority")
		}
		other := r
		other.ID = "admitted-during-effect"
		_, err := s.Enqueue(ctx, other)
		return err
	}
	done, err := s.Execute(ctx, r.Path, r.ID)
	if err != nil || done.Item.State != Integrated || held {
		t.Fatalf("happy path: %+v %v held=%v", done, err, held)
	}
	if got := gitTest(t, r.Path, "rev-parse", "main"); got != r.Commit {
		t.Fatalf("target=%s", got)
	}
	other, err := s.Status(ctx, r.Path, "admitted-during-effect")
	if err != nil || other.Item.State != Pending || other.Count != 2 {
		t.Fatalf("concurrent admission lost: %+v %v", other, err)
	}
}

func TestExecutionRejectsDeferredAuthorityCallback(t *testing.T) {
	s, r, _ := runtimeFixture(t)
	ctx := context.Background()
	if _, err := s.Enqueue(ctx, r); err != nil {
		t.Fatal(err)
	}
	var delayed func() error
	s.withAuthority = func(ctx context.Context, item RuntimeItem, effect func() error) error { delayed = effect; return nil }
	if _, err := s.Execute(ctx, r.Path, r.ID); err == nil {
		t.Fatal("missing synchronous authority accepted")
	}
	if err := delayed(); err == nil {
		t.Fatal("callback ran outside authority lifetime")
	}
	if gitTest(t, r.Path, "rev-parse", "main") != r.TargetHead {
		t.Fatal("expired callback mutated Git")
	}
}
