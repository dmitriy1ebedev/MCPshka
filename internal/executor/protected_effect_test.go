package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func protectedEffectFixture(t *testing.T) (*Controller, string, IntegrationApproval, *os.Root) {
	t.Helper()
	c, parent := protectedBirthFixture(t)
	born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	commit, _ := protectedTestCommit(t, born.Repository.CommonDir)
	root, err := os.OpenRoot(born.Repository.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	_, payload, _, err := readProtectedObject(context.Background(), root, commit, protectedObjectBytes)
	if err != nil {
		t.Fatal(err)
	}
	links, err := protectedObjectLinks("commit", payload)
	if err != nil {
		t.Fatal(err)
	}
	var expected string
	for _, link := range links {
		if link.Kind == "commit" {
			expected = link.OID
		}
	}
	_, _, item := integrationApprovalFixture(t)
	item.Request.Commit = commit
	item.Request.TargetHead = expected
	a := IntegrationApproval{ID: integrationApprovalID(item), Repository: born.Repository, Request: item.Request, ExecutionID: item.ExecutionID}
	if err := publishProtectedRef(context.Background(), root, "refs/heads/main", expected, true); err != nil {
		t.Fatal(err)
	}
	return c, parent, a, root
}

func TestProtectedEffectCASCompletionReplayAndGeneration(t *testing.T) {
	c, parent, a, root := protectedEffectFixture(t)
	ctx := context.Background()
	if err := c.applyProtectedRefEffect(ctx, parent, a, 1, uint32(os.Geteuid()), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.observeProtectedIntegration(ctx, parent, a, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/main"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.applyProtectedRefEffect(ctx, parent, a, 1, uint32(os.Geteuid()), nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/main"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("replay rewrote target", err)
	}
	if err := c.applyProtectedRefEffect(ctx, parent, a, 2, uint32(os.Geteuid()), nil); !errors.Is(err, ErrHold) {
		t.Fatal("old effect borrowed new generation", err)
	}
	head, err := readProtectedDirectRef(root, "refs/heads/main")
	if err != nil || head != a.Request.Commit {
		t.Fatal(head, err)
	}
}

func TestProtectedEffectCrashAfterTargetDoesNotRepeatUpdate(t *testing.T) {
	c, parent, a, _ := protectedEffectFixture(t)
	ctx := context.Background()
	crash := errors.New("isolated crash after durable target")
	if err := c.applyProtectedRefEffect(ctx, parent, a, 1, uint32(os.Geteuid()), func() error { return crash }); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/main"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(a.ExecutionID))
	if _, err := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/mcpshka/integration", hex.EncodeToString(sum[:]))); !os.IsNotExist(err) {
		t.Fatal("receipt published before crash", err)
	}
	if err := c.applyProtectedRefEffect(ctx, parent, a, 1, uint32(os.Geteuid()), func() error { t.Fatal("target update repeated"); return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(a.Repository.CommonDir, "refs/heads/main"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("crash recovery rewrote target", err)
	}
}

func TestProtectedEffectUnknownBeforeTargetKeepsHold(t *testing.T) {
	c, parent, a, root := protectedEffectFixture(t)
	intent := protectedRefEffect{Version: 1, Repository: a.Repository, ApprovalID: a.ID, ExecutionID: a.ExecutionID, Generation: 1, Ref: "refs/heads/main", Expected: a.Request.TargetHead, Candidate: a.Request.Commit, State: "ATTEMPT"}
	raw, _ := json.Marshal(intent)
	if err := c.db.Put(protectedEffectKey(a.ExecutionID), raw); err != nil {
		t.Fatal(err)
	}
	if err := c.applyProtectedRefEffect(context.Background(), parent, a, 1, uint32(os.Geteuid()), nil); !errors.Is(err, ErrHold) {
		t.Fatal("unknown update retried", err)
	}
	head, err := readProtectedDirectRef(root, "refs/heads/main")
	if err != nil || head != a.Request.TargetHead {
		t.Fatal("target changed", err)
	}
}

func TestProtectedEffectRejectsChangedHeadAndDivergedCommit(t *testing.T) {
	for _, kind := range []string{"changed-head", "diverged"} {
		t.Run(kind, func(t *testing.T) {
			c, parent, a, root := protectedEffectFixture(t)
			tree := writeProtectedTestObject(t, a.Repository.CommonDir, "tree", nil)
			other := writeProtectedTestObject(t, a.Repository.CommonDir, "commit", []byte("tree "+tree+"\n\nunrelated\n"))
			if err := publishProtectedRef(context.Background(), root, "refs/heads/main", other, false); err != nil {
				t.Fatal(err)
			}
			if kind == "diverged" {
				a.Request.TargetHead = other
			}
			if err := c.applyProtectedRefEffect(context.Background(), parent, a, 1, uint32(os.Geteuid()), nil); !errors.Is(err, ErrHold) {
				t.Fatal("conflict integrated", err)
			}
			head, err := readProtectedDirectRef(root, "refs/heads/main")
			if err != nil || head != other {
				t.Fatal("conflicting head overwritten", err)
			}
		})
	}
}

func TestProtectedEffectConflictingMarkerNeverAdvancesTarget(t *testing.T) {
	c, parent, a, root := protectedEffectFixture(t)
	sum := sha256.Sum256([]byte(a.ExecutionID))
	receipt := "refs/mcpshka/integration/" + hex.EncodeToString(sum[:])
	if err := publishProtectedRef(context.Background(), root, receipt, a.Request.TargetHead, true); err != nil {
		t.Fatal(err)
	}
	if err := c.applyProtectedRefEffect(context.Background(), parent, a, 1, uint32(os.Geteuid()), nil); !errors.Is(err, ErrHold) {
		t.Fatal("conflicting marker accepted", err)
	}
	head, err := readProtectedDirectRef(root, "refs/heads/main")
	if err != nil || head != a.Request.TargetHead {
		t.Fatal("target advanced despite receipt conflict", err)
	}
}

func TestProtectedEffectRecoversFailedTargetAndReceiptDirectorySync(t *testing.T) {
	for _, where := range []string{"target", "receipt"} {
		t.Run(where, func(t *testing.T) {
			c, parent, a, root := protectedEffectFixture(t)
			sum := sha256.Sum256([]byte(a.ExecutionID))
			target := "refs/heads/main"
			receipt := "refs/mcpshka/integration/" + hex.EncodeToString(sum[:])
			failRef := target
			if where == "receipt" {
				failRef = receipt
			}
			// ENOTDIR fails equally for root and an unprivileged test runner.
			broken := t.TempDir()
			if err := os.WriteFile(filepath.Join(broken, "file"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			brokenRoot, err := os.OpenRoot(broken)
			if err != nil {
				t.Fatal(err)
			}
			defer brokenRoot.Close()
			fault := ErrHold
			failed := false
			sync := func(ref string) error {
				if ref == failRef && !failed {
					failed = true
					return syncProtectedRefDirectory(brokenRoot, "file/parent/ref")
				}
				return syncProtectedRefDirectory(root, ref)
			}
			if err := c.applyProtectedRefEffectWithSync(context.Background(), parent, a, 1, uint32(os.Geteuid()), nil, sync); !errors.Is(err, fault) {
				t.Fatal(err)
			}
			if !failed {
				t.Fatal("directory sync fault was not reached")
			}
			before, err := os.Stat(filepath.Join(a.Repository.CommonDir, target))
			if err != nil {
				t.Fatal(err)
			}
			var synced []string
			recoverSync := func(ref string) error { synced = append(synced, ref); return syncProtectedRefDirectory(root, ref) }
			if err := c.applyProtectedRefEffectWithSync(context.Background(), parent, a, 1, uint32(os.Geteuid()), nil, recoverSync); err != nil {
				t.Fatal(err)
			}
			if len(synced) != 2 || synced[0] != target || synced[1] != receipt {
				t.Fatalf("completion without ordered durability: %+v", synced)
			}
			after, err := os.Stat(filepath.Join(a.Repository.CommonDir, target))
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("recovery repeated target rename", err)
			}
		})
	}
}

func TestProtectedEffectNeverAdoptsUnjournaledCandidate(t *testing.T) {
	c, parent, a, root := protectedEffectFixture(t)
	if err := publishProtectedRef(context.Background(), root, "refs/heads/main", a.Request.Commit, false); err != nil {
		t.Fatal(err)
	}
	if err := c.applyProtectedRefEffect(context.Background(), parent, a, 1, uint32(os.Geteuid()), nil); !errors.Is(err, ErrHold) {
		t.Fatal("unjournaled effect adopted", err)
	}
	if _, err := c.db.GetContext(context.Background(), protectedEffectKey(a.ExecutionID)); err == nil {
		t.Fatal("post-effect intent minted")
	}
}
