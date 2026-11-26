package projectlease

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/idempotency"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func TestAtomicLeaseAndCredentialReceiptShareOneCommit(t *testing.T) {
	for _, outcome := range []string{"ok", "before", "after", "occupied", "gate", "different-db"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			db, err := statestore.Open(filepath.Join(root, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			s := New(filepath.Join(root, "leases"), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
			store := idempotency.NewWithDatabase(filepath.Join(root, "idempotency"), db)
			if outcome == "different-db" {
				other, err := statestore.Open(filepath.Join(t.TempDir(), "other.db"))
				if err != nil {
					t.Fatal(err)
				}
				store = idempotency.NewWithDatabase("other", other)
			}
			const key = "principal-a-original"
			const op = "project_lease_acquire_atomic/v1"
			fp, err := idempotency.Fingerprint(op, map[string]string{"path": root, "owner": "owner"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Begin(key, op, fp); err != nil {
				t.Fatal(err)
			}
			var previous AcquireResult
			if outcome == "occupied" {
				previous, err = s.AcquireTracked(root, "other", strings.Repeat("b", 32))
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.AcquireJournaled(root, "owner", strings.Repeat("a", 32), func(authority *statestore.DB, recordKey string, data []byte, gate string, result AcquireResult) error {
				if outcome == "before" {
					return errors.New("fault before commit")
				}
				if outcome == "gate" {
					if gate == "" {
						t.Skip("root bypasses unprivileged writer gate")
					}
					if ok, err := authority.PutIfAbsent(gate, []byte("fenced")); err != nil || !ok {
						t.Fatal(ok, err)
					}
				}
				if err := store.CompleteWithRecord(authority, key, op, fp, result, recordKey, data, gate); err != nil {
					return err
				}
				if outcome == "after" {
					return errors.New("lost reply after atomic commit")
				}
				return nil
			})
			if (outcome == "ok") != (err == nil) {
				t.Fatal("unexpected admission outcome", err)
			}
			var recovered AcquireResult
			state, readErr := store.ReadResult(key, op, fp, &recovered)
			if readErr != nil {
				t.Fatal(readErr)
			}
			committed := outcome == "ok" || outcome == "after"
			if committed {
				if state != idempotency.Completed || recovered.Token == "" {
					t.Fatal("held without recoverable credential", state)
				}
				cold := New(filepath.Join(root, "leases"), s.policy)
				if _, err := cold.ValidateHolder(root, recovered.Token); err != nil {
					t.Fatal("cold credential invalid", err)
				}
				if _, err := cold.Release(root, recovered.Token); err != nil {
					t.Fatal(err)
				}
				if _, err := cold.ValidateHolder(root, recovered.Token); err == nil {
					t.Fatal("released receipt resurrected authority")
				}
			} else {
				if state != idempotency.Pending {
					t.Fatal("failed admission completed receipt", state)
				}
				status, err := s.Status(root)
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "occupied" {
					if status.ExecutionID != previous.Status.ExecutionID {
						t.Fatal("changed existing holder")
					}
				} else if status.Held {
					t.Fatal("failure stranded lease", status)
				}
			}
		})
	}
}
