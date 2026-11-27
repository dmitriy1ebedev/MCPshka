package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func protectedInitialFixture(t *testing.T) (*Controller, string, IntegrationApproval, *os.Root) {
	c, parent, a, root := protectedEffectFixture(t)
	if err := root.Remove("refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	return c, parent, a, root
}
func TestProtectedTargetInitializationAndReplay(t *testing.T) {
	c, parent, a, root := protectedInitialFixture(t)
	ctx := context.Background()
	if err := c.initializeProtectedTarget(ctx, parent, a, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/main"))
	if err := c.initializeProtectedTarget(ctx, parent, a, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/main"))
	if !os.SameFile(before, after) {
		t.Fatal("replay replaced initialized target")
	}
	if err := c.applyProtectedRefEffect(ctx, parent, a, 1, uint32(os.Geteuid()), nil); err != nil {
		t.Fatal(err)
	}
	a.Request.TargetHead = a.Request.Commit
	if err := c.initializeProtectedTarget(ctx, parent, a, uint32(os.Geteuid())); err != nil {
		t.Fatal("advanced target was reset", err)
	}
	got, err := readProtectedDirectRef(root, "refs/heads/main")
	if err != nil || got != a.Request.Commit {
		t.Fatal(got, err)
	}
}
func TestProtectedTargetRejectsUnjournaledExistingRef(t *testing.T) {
	c, parent, a, _ := protectedEffectFixture(t)
	if err := c.initializeProtectedTarget(context.Background(), parent, a, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatal("adopted existing ref", err)
	}
	if _, err := c.db.Get(protectedTargetKey(a.Repository, "refs/heads/main")); !os.IsNotExist(err) {
		t.Fatal("post-effect intent minted", err)
	}
}
func TestProtectedTargetRecoveryBeforeAndAfterPublication(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[published], func(t *testing.T) {
			c, parent, a, root := protectedInitialFixture(t)
			intent := protectedTarget{Version: 1, Repository: a.Repository, Ref: "refs/heads/main", Initial: a.Request.TargetHead, State: "CREATE_INTENT"}
			raw, _ := json.Marshal(intent)
			if err := c.db.Put(protectedTargetKey(a.Repository, intent.Ref), raw); err != nil {
				t.Fatal(err)
			}
			if published {
				if err := publishProtectedRef(context.Background(), root, intent.Ref, intent.Initial, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.initializeProtectedTarget(context.Background(), parent, a, uint32(os.Geteuid())); err != nil {
				t.Fatal(err)
			}
			got, err := readProtectedDirectRef(root, intent.Ref)
			if err != nil || got != intent.Initial {
				t.Fatal(got, err)
			}
		})
	}
}
func TestProtectedTargetRecoveryAfterFailedDirectorySync(t *testing.T) {
	c, parent, a, _ := protectedInitialFixture(t)
	failure := errors.New("injected post rename directory sync failure")
	if err := c.initializeProtectedTargetWithSync(context.Background(), parent, a, uint32(os.Geteuid()), func(string) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/main"))
	calls := 0
	if err := c.initializeProtectedTargetWithSync(context.Background(), parent, a, uint32(os.Geteuid()), func(string) error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/main"))
	if calls != 1 || !os.SameFile(before, after) {
		t.Fatal("recovery failed to resync without replacing")
	}
}
func TestProtectedTargetCorruptOrWrongIntentDoesNotWrite(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "wrong"}[wrong], func(t *testing.T) {
			c, parent, a, root := protectedInitialFixture(t)
			raw := []byte("{")
			if wrong {
				raw, _ = json.Marshal(protectedTarget{Version: 1, Repository: a.Repository, Ref: "refs/heads/main", Initial: a.Request.Commit, State: "CREATE_INTENT"})
			}
			if err := c.db.Put(protectedTargetKey(a.Repository, "refs/heads/main"), raw); err != nil {
				t.Fatal(err)
			}
			if err := c.initializeProtectedTarget(context.Background(), parent, a, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
				t.Fatal(err)
			}
			if _, err := root.Stat("refs/heads/main"); !os.IsNotExist(err) {
				t.Fatal("bad intent published ref", err)
			}
		})
	}
}
