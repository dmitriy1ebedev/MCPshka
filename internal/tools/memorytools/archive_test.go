package memorytools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/memory"
)

func TestArchiveUsesLegacyBaselineAndOneAdmissionPerSource(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	s, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, pending := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, dir := range archiveDirs {
		p := filepath.Join(root, dir)
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "checkpoint.json"), []byte(fmt.Sprintf(`{"processed":{"%s":{}}}`, old)), 0600); err != nil {
			t.Fatal(err)
		}
		rows := fmt.Sprintf("{\"sha256\":%q,\"text\":\"already considered\"}\n{\"sha256\":%q,\"text\":\"new archive input\"}\n", old, pending)
		if err := os.WriteFile(filepath.Join(p, "source.jsonl"), []byte(rows), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first := archiveInput{Source: "claude", Action: "start", ClaimKey: "scheduled-key-0001"}
	start, err := runArchive(ctx, s, nil, root, "owner", first)
	if err != nil || start.Remaining != 1 || len(start.Items) != 1 || start.Items[0].SHA != pending {
		t.Fatal(start, err)
	}
	manual := archiveInput{Source: "claude", Action: "start", ClaimKey: "manual-key-0000001"}
	second, err := runArchive(ctx, s, nil, root, "owner", manual)
	if err != nil || second.Run.State != "busy" || second.Run.ID != start.Run.ID || len(second.Items) > 0 {
		t.Fatal(second, err)
	}
	manual.Source = "codex"
	if other, err := runArchive(ctx, s, nil, root, "owner", manual); err != nil || other.Run.State != "running" {
		t.Fatal(other, err)
	}
	first.Action = "finish"
	finished, err := runArchive(ctx, s, nil, root, "owner", first)
	if err != nil || finished.Run.State != "no_effect" || finished.Run.NextSafeAction == "" {
		t.Fatal(finished, err)
	}

	first = archiveInput{Source: "claude", Action: "start", ClaimKey: "scheduled-key-0002"}
	if _, err := runArchive(ctx, s, nil, root, "owner", first); err != nil {
		t.Fatal(err)
	}
	first.Action = "commit"
	first.Decision = &memory.ArchiveDecision{SHA: pending, Class: "nonactionable", Reason: "No durable lesson in the excerpt."}
	commit, err := runArchive(ctx, s, nil, root, "owner", first)
	if err != nil || commit.Receipt.SHA != pending {
		t.Fatal(commit, err)
	}
	first.Action, first.Decision = "next", nil
	done, err := runArchive(ctx, s, nil, root, "owner", first)
	if err != nil || done.Run.State != "completed" || done.Run.Processed != 1 || !done.Run.Effect || done.Remaining != 0 {
		t.Fatal(done, err)
	}
	first.Action, first.ClaimKey = "start", "scheduled-key-0003"
	idle, err := runArchive(ctx, s, nil, root, "owner", first)
	if err != nil || idle.Run.State != "idle" || idle.Run.Effect {
		t.Fatal(idle, err)
	}
}
