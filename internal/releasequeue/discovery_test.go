package releasequeue

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/authn"
	"mcpshka/internal/statestore"
)

func TestReleaseQueueDiscoversOnlyCleanIdleNewerWorktree(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	const path = "/root/repos/wt/mcpshka-release-0.17.10"
	const branch = "release/0.17.10"
	head := strings.Repeat("a", 40)
	original := readReleaseGit
	t.Cleanup(func() { readReleaseGit = original })
	clean, idle := false, true
	readReleaseGit = func(_ context.Context, root string, args ...string) (string, error) {
		switch args[0] {
		case "worktree":
			return "worktree " + path + "\x00HEAD " + head + "\x00branch refs/heads/" + branch + "\x00\x00", nil
		case "show":
			return "0.17.10\n", nil
		case "status":
			if !clean {
				return " M VERSION\n", nil
			}
			return "", nil
		case "rev-parse":
			return head + "\n", nil
		}
		t.Fatalf("unexpected Git probe: %s %v", root, args)
		return "", nil
	}
	w := &Watcher{DB: db, Now: time.Now, Installed: func(context.Context) string { return "0.17.2" },
		DiscoveryIdle: func(string) bool { return idle },
		Call: func(_ context.Context, name string, _ map[string]any) (map[string]any, error) {
			t.Fatalf("an unapproved discovery invoked %s", name)
			return nil, nil
		}}
	ctx := context.Background()
	w.Tick(ctx)
	clean, idle = true, false
	w.Tick(ctx)
	if items, err := Load(ctx, db); err != nil || len(items) != 0 {
		t.Fatalf("dirty or busy observation was enqueued: %v %v", items, err)
	}
	idle = true
	w.Tick(ctx)
	w.Tick(ctx)
	items, err := Load(ctx, db)
	if err != nil || len(items) != 1 || items[0].Stage != "DISCOVERED" || items[0].Commit != head {
		t.Fatalf("expected one exact release observation, got %v %v", items, err)
	}
	view, err := Mutate(db, authn.Identity{ID: "owner"}, Input{
		Action: "queue", Version: "0.17.10", Commit: head, Branch: branch, StoreID: strings.Repeat("b", 32),
	}, time.Now())
	if err != nil || view.Stage != "QUEUED" || view.Owner != "owner" {
		t.Fatalf("authenticated admission returned %+v %v", view, err)
	}
}
