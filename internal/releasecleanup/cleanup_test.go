package releasecleanup

import (
	"context"
	"errors"
	"mcpshka/internal/failure"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReleaseCleanupProtectsUninstalledEvidenceBUG82(t *testing.T) {
	builds := []Build{{Permit: "new-build", Commit: "pending"}, {Permit: "old-build", Commit: "old", Image: "old-image"}, {Permit: "current-build", Commit: "current", Image: "current-image"}}
	manifests := map[string]string{"gitverse-test": "pending", "old-test": "old", "current-test": "current", "unknown": "other"}
	keep := ProtectedPermits(builds, manifests, map[string]bool{"old-image": true, "current-image": true}, [2]string{"current-image", "rollback-image"})
	for _, id := range []string{"new-build", "gitverse-test", "current-build", "current-test"} {
		if !keep[id] {
			t.Errorf("release dependency %s unprotected", id)
		}
	}
	if keep["old-build"] || keep["old-test"] {
		t.Fatal("obsolete installed release retained")
	}
}

func TestReleaseCleanupOnlyCurrentAndRollback(t *testing.T) {
	builds := []Build{{Permit: "a", Commit: "a", Image: "a"}, {Permit: "b", Commit: "b", Image: "b"}, {Permit: "c", Commit: "c", Image: "c"}}
	keep := ProtectedPermits(builds, map[string]string{"ta": "a", "tb": "b", "tc": "c"}, map[string]bool{"a": true, "b": true, "c": true}, [2]string{"c", "b"})
	if keep["a"] || keep["ta"] || !keep["b"] || !keep["tb"] || !keep["c"] || !keep["tc"] {
		t.Fatalf("wrong retained set: %v", keep)
	}
}

func TestCleanupRefusesSymlinkTree(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.WriteFile(data, []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(base, "tree")
	os.Mkdir(tree, 0700)
	os.Symlink(data, filepath.Join(tree, "link"))
	if err := Remove(context.Background(), tree); !errors.Is(err, failure.ErrHold) {
		t.Fatalf("symlink tree accepted: %v", err)
	}
	if raw, err := os.ReadFile(data); err != nil || string(raw) != "user data" {
		t.Fatal("followed cleanup symlink", err)
	}
}

// Expire precisely after the three-entry validation walk, before removal.
type cleanupRemovalDeadline struct {
	context.Context
	checks int
}

func (c *cleanupRemovalDeadline) Err() error {
	c.checks++
	if c.checks > 3 {
		return context.DeadlineExceeded
	}
	return nil
}
func TestCleanupRemovalChecksBudgetAfterTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(path, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &cleanupRemovalDeadline{Context: context.Background()}
	if err := Remove(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("removal ignored exhausted budget:", err)
	}
	if _, err := os.Stat(filepath.Join(path, "a")); err != nil {
		t.Fatal("removed after budget expired:", err)
	}
}

func TestCleanupLargeTreeMakesProgressAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4200; i++ {
		if err := os.WriteFile(filepath.Join(path, strconv.Itoa(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := Remove(context.Background(), path)
	if err == nil {
		t.Fatal("large tree should yield to the next bounded pass")
	}
	files, readErr := os.ReadDir(path)
	if readErr != nil || len(files) == 0 || len(files) >= 4200 {
		t.Fatal("no bounded progress", len(files), err, readErr)
	}
	if err := Remove(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestRetainedReleaseVersionsBUG109(t *testing.T) {
	upgrades := []Upgrade{
		{Version: "0.1.5", State: "SUCCEEDED"},
		{Version: "0.1.4", State: "SUCCEEDED"},
		{Version: "0.1.3", State: "SUCCEEDED"},
		{Version: "0.1.2", State: "SUCCEEDED"},
		{Version: "0.1.1", State: "SUCCEEDED"},
	}
	obsolete := ObsoleteVersions(upgrades, "0.1.2", "0.1.1", 2)
	if !obsolete["0.1.3"] || obsolete["0.1.5"] || obsolete["0.1.4"] || obsolete["0.1.2"] || obsolete["0.1.1"] {
		t.Fatalf("current/rollback and newest N: %v", obsolete)
	}
	upgrades = append(upgrades, Upgrade{Version: "0.1.6", State: "SWAPPING"})
	if got := ObsoleteVersions(upgrades, "0.1.2", "0.1.1", 2); got["0.1.6"] || !got["0.1.3"] {
		t.Fatalf("unfinished version must be protected without pinning unrelated history: %v", got)
	}
}
