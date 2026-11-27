package codexcoord

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/statestore"
)

func FuzzQueueRecoveryTransitions(f *testing.F) {
	for _, seed := range [][]byte{{0, 1, 1, 2}, {2, 3, 1}, {255, 2}, {255, 1}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) == 0 || len(operations) > 16 {
			return
		}
		directory := t.TempDir()
		db, err := statestore.Open(filepath.Join(directory, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		q, err := New(db)
		if err != nil {
			t.Fatal(err)
		}
		catalog := []Model{{ID: "model", Efforts: []string{"high"}}, {ID: "other", Efforts: []string{"high"}}}
		initial := Task{ID: "fuzz-task", Provider: "codex", SessionID: "session_fuzz_123", Worktree: filepath.Join(directory, "repo"), RoadmapSHA: strings.Repeat("a", 64), Message: "isolated recovery", Model: "model", Effort: "high"}
		current, err := q.Enqueue(initial, catalog)
		if err != nil {
			t.Fatal(err)
		}
		if operations[0] == 255 {
			before, _ := json.Marshal(current)
			current.Revision = math.MaxUint64
			after, _ := json.Marshal(current)
			if ok, err := db.Replace(key(current.ID), before, after); err != nil || !ok {
				t.Fatal("fixture CAS failed")
			}
			operations = operations[1:]
		}
		for _, operation := range operations {
			before, err := q.Get(initial.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch operation % 4 {
			case 0:
				_, err = q.Enqueue(initial, catalog)
			case 1:
				_, err = q.Reserve(initial.ID, before.Revision, "dispatch-fuzz")
			case 2:
				_, err = q.Reconfigure(initial.ID, before.Revision, "other", "high", catalog)
			case 3:
				_, err = q.Suspend(initial.ID, before.Revision)
			}
			after, readErr := q.Get(initial.ID)
			if readErr != nil {
				t.Fatal("transition made durable receipt unreadable", readErr)
			}
			if err != nil && after != before {
				t.Fatal("rejected transition changed task")
			}
			if after.Revision < before.Revision {
				t.Fatal("revision wrapped or moved backwards")
			}
			recovered, err := New(db)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := recovered.Get(initial.ID)
			if err != nil || persisted != after {
				t.Fatal("recovery lost task state")
			}
			if after.State == Reserved {
				receipt, err := recovered.InspectReservation(initial.ID)
				if err != nil || receipt.DispatchEnabled || !receipt.ClaimsVerified {
					t.Fatal("reservation fabricated execution or lost ownership")
				}
			}
		}
	})
}
