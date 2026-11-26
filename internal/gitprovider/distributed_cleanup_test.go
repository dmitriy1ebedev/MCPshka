package gitprovider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrunePublicCIOnlyAfterThreeDays(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	commit := strings.Repeat("a", 40)
	sha := strings.Repeat("b", 40)
	dir := filepath.Join(root, commit)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, age := range map[string]time.Duration{"github-0": 73 * time.Hour, "gitverse-1": 71 * time.Hour, "gitflic-2": 80 * time.Hour, "github-4": 80 * time.Hour} {
		path := filepath.Join(dir, name)
		stamp := now.Add(-age)
		if err := os.WriteFile(path, []byte(stamp.Format(time.RFC3339Nano)+"\n"+sha+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	err := pruneCIJournals(context.Background(), root, now, func(_ context.Context, site Site, ref, oid string) error {
		calls++
		if site != GitHub || ref != "refs/heads/ci/aaaaaaaaaaaa/shard-0" || oid != sha {
			t.Fatalf("unexpected deletion: %s %s", site, ref)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("cleanup calls=%d err=%v", calls, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "github-0")); !os.IsNotExist(err) {
		t.Fatal("pruned ref's receipt remains")
	}
}
