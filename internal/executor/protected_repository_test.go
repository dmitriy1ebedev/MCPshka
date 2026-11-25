package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func protectedBirthFixture(t *testing.T) (*Controller, string) {
	t.Helper()
	if os.Geteuid() == 0 || os.Getenv("MCPSHKA_EXECUTOR_TEST_AS_ROOT") == "service-user" {
		t.Skip("BUG-20261020-10: protected/candidate birth and fence fixtures require a native unprivileged service-user run; root and its nobody re-exec are not supported")
	}
	previous := protectedImportStatfs
	protectedImportStatfs = func(_ int, st *syscall.Statfs_t) error {
		*st = syscall.Statfs_t{Bsize: 4096, Blocks: 20 << 18, Bavail: 10 << 18}
		return nil
	}
	t.Cleanup(func() { protectedImportStatfs = previous })
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	return New(db), parent
}

const protectedBirthID = "0123456789abcdef0123456789abcdef"

func TestProtectedRepositoryBirthAndReplay(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	ctx := context.Background()
	born, err := c.prepareProtectedRepository(ctx, parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if born.State != "READY" || born.Repository.Device == 0 || born.Repository.Inode == 0 {
		t.Fatalf("unsealed birth: %+v", born)
	}
	replay, err := c.prepareProtectedRepository(ctx, parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil || replay != born {
		t.Fatalf("replay changed identity: %+v %v", replay, err)
	}
	for _, path := range []string{"objects", "refs/heads", "refs/tags"} {
		if st, err := os.Stat(filepath.Join(born.Repository.CommonDir, path)); err != nil || !st.IsDir() {
			t.Fatalf("missing bare storage %s: %v", path, err)
		}
	}
}

func TestProtectedRepositoryBirthNeverAdoptsExistingDirectory(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	path := filepath.Join(parent, protectedBirthID+".git")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	if _, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatalf("adopted interrupted/unverified storage: %v", err)
	}
}

func TestProtectedRepositoryReplayRejectsIndirectionAndTampering(t *testing.T) {
	for _, kind := range []string{"alternates", "config-include", "identity-replacement", "writable-parent"} {
		t.Run(kind, func(t *testing.T) {
			c, parent := protectedBirthFixture(t)
			born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "alternates":
				err = os.WriteFile(filepath.Join(born.Repository.CommonDir, "objects/info/alternates"), []byte("/tmp/foreign-objects\n"), 0600)
			case "config-include":
				err = os.WriteFile(filepath.Join(born.Repository.CommonDir, "config"), []byte("[include]\npath = /tmp/foreign-config\n"), 0600)
			case "identity-replacement":
				err = os.Rename(born.Repository.CommonDir, born.Repository.CommonDir+".preserved")
				if err == nil {
					err = os.Mkdir(born.Repository.CommonDir, 0700)
				}
			case "writable-parent":
				err = os.Chmod(parent, 0770)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
				t.Fatalf("unsafe replay %s: %v", kind, err)
			}
		})
	}
}

func TestProtectedRepositoryBirthRejectsUntrustedParentAndCancellation(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	if err := os.Chmod(parent, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, protectedBirthID+".git")); !os.IsNotExist(err) {
		t.Fatalf("mutated unsafe parent: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.prepareProtectedRepository(ctx, parent, protectedBirthID, uint32(os.Geteuid())); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestProtectedRepositoryRecoversIntentBeforeMkdir(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	identity, err := protectedDirectoryIdentity(parent, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	intent := ProtectedRepository{Version: 1, ID: protectedBirthID, State: "CREATE_INTENT", Parent: identity, Repository: RepositoryIdentity{CommonDir: filepath.Join(parent, protectedBirthID+".git")}}
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.db.Put(protectedRepositoryKey(protectedBirthID), raw); err != nil {
		t.Fatal(err)
	}
	born, err := c.prepareProtectedRepository(context.Background(), parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil || born.State != "READY" {
		t.Fatalf("intent recovery: %+v %v", born, err)
	}
}

func TestProtectedRepositoryBirthAuthorityRejectsMissingAndMismatchedRecords(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	ctx := context.Background()
	born, err := c.prepareProtectedRepository(ctx, parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, born.Repository, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	wrong := born.Repository
	wrong.Inode++
	if _, err := c.protectedRepositoryBirth(ctx, parent, wrong, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatalf("wrong identity admitted: %v", err)
	}
	if _, err := c.protectedRepositoryBirth(ctx, filepath.Dir(parent), born.Repository, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatalf("wrong parent admitted: %v", err)
	}
	empty, err := statestore.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(empty).protectedRepositoryBirth(ctx, parent, born.Repository, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatalf("metadata without provenance admitted: %v", err)
	}
	born.State = "CREATE_INTENT"
	raw, err := json.Marshal(born)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.db.Put(protectedRepositoryKey(protectedBirthID), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, born.Repository, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatalf("incomplete birth admitted: %v", err)
	}
}

func TestProtectedRepositoryObserveExactTargetAndReceipt(t *testing.T) {
	c, parent := protectedBirthFixture(t)
	ctx := context.Background()
	born, err := c.prepareProtectedRepository(ctx, parent, protectedBirthID, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	_, _, item := integrationApprovalFixture(t)
	commit, _ := protectedTestCommit(t, born.Repository.CommonDir)
	item.Request.Commit = commit
	a := IntegrationApproval{Repository: born.Repository, Request: item.Request, ExecutionID: item.ExecutionID}
	if _, err := c.observeProtectedIntegration(ctx, parent, a, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatalf("missing effect accepted: %v", err)
	}
	sum := sha256.Sum256([]byte(a.ExecutionID))
	receipt := filepath.Join(born.Repository.CommonDir, "refs/mcpshka/integration", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(filepath.Dir(receipt), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(born.Repository.CommonDir, "refs/heads/main")
	if err := os.WriteFile(target, []byte(a.Request.Commit+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.observeProtectedIntegration(ctx, parent, a, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
		t.Fatalf("target without receipt accepted: %v", err)
	}
	if err := os.WriteFile(receipt, []byte(a.Request.Commit+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := c.observeProtectedIntegration(ctx, parent, a, uint32(os.Geteuid()))
	if err != nil || got.Repository != born.Repository || got.Head != a.Request.Commit || got.Receipt != a.Request.Commit {
		t.Fatalf("exact effect rejected: %+v %v", got, err)
	}
	for _, bad := range []string{strings.Repeat("8", 40) + "\n", "ref: refs/heads/main\n", a.Request.Commit + "\ntrailing"} {
		if err := os.WriteFile(receipt, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.observeProtectedIntegration(ctx, parent, a, uint32(os.Geteuid())); !errors.Is(err, ErrHold) {
			t.Fatalf("invalid receipt accepted: %q %v", bad, err)
		}
	}
}
