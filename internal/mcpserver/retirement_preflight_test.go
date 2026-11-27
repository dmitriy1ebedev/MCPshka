package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/idempotency"
	"mcpshka/internal/statestore"
)

func TestRetirementPreflightRejectsBeforeReceiptAndReconcilesOnlyInvalidPending(t *testing.T) {
	const key, operation = "contract:test-retirement", "contract/admin_session_retire_deployment"
	for _, tc := range []struct {
		name, reason, prior string
		conflict            bool
		legacy              bool
		want                idempotency.State
	}{
		{name: "fresh invalid", reason: strings.Repeat("x", 201)},
		{name: "invalid pending", reason: strings.Repeat("x", 201), prior: "pending", want: idempotency.Failed},
		{name: "valid unknown", reason: "Approved historical retirement", prior: "pending", want: idempotency.Pending},
		{name: "conflicting pending", reason: strings.Repeat("x", 201), prior: "pending", conflict: true, want: idempotency.Pending},
		{name: "completed protected", reason: strings.Repeat("x", 201), prior: "completed", want: idempotency.Completed},
		{name: "legacy backend held", reason: strings.Repeat("x", 201), prior: "pending", legacy: true, want: idempotency.Pending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			db, err := statestore.Open(filepath.Join(root, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			store := idempotency.NewWithDatabase(filepath.Join(root, "idempotency"), db)
			if tc.legacy {
				store = idempotency.New(filepath.Join(root, "legacy"))
			}
			r := &toolRegistry{store: store}
			input := map[string]any{"reason": tc.reason}
			fp, err := idempotency.Fingerprint("admin_session_retire_deployment", input)
			if err != nil {
				t.Fatal(err)
			}
			storedFP := fp
			if tc.conflict {
				storedFP = strings.Repeat("a", 64)
			}
			if tc.prior != "" {
				if _, err := store.Begin(key, operation, storedFP); err != nil {
					t.Fatal(err)
				}
			}
			if tc.prior == "completed" {
				if err := store.Complete(key, operation, storedFP, map[string]any{"done": true}); err != nil {
					t.Fatal(err)
				}
			}
			err = r.retirementPreflight("admin_session_retire_deployment", input, key, operation, fp)
			if (len(tc.reason) > 200) != (err != nil) {
				t.Fatalf("unexpected preflight error: %v", err)
			}
			s, e := store.Lookup(key)
			if tc.prior == "" {
				if !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("invalid request allocated receipt: %+v/%v", s, e)
				}
				return
			}
			if e != nil || s.State != tc.want || s.Fingerprint != storedFP {
				t.Fatalf("unsafe reconciliation: %+v/%v", s, e)
			}
		})
	}
}

func TestRetirementRejectedProbeNeverAuthorizesRetry(t *testing.T) {
	for _, marked := range []bool{false, true} {
		m, _, _ := yieldFixture(t)
		client := uploadClient(t, m, "holder", "operator")
		ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "holder", Role: "operator", Scopes: []string{"*"}})
		key := mutationReceiptKey(ctx, "admin_session_retire_deployment", "rejected-v1", nil)
		sum := sha256.Sum256([]byte(key))
		ledger := "contract:" + hex.EncodeToString(sum[:])
		store := idempotency.NewWithDatabase(t.TempDir(), m.Database())
		if _, err := store.Begin(ledger, "contract/admin_session_retire_deployment", "pinned"); err != nil {
			t.Fatal(err)
		}
		message := "arbitrary failure may have effects"
		if marked {
			message = retirementRejectedBeforeWorker + "invalid reason"
		}
		if err := store.FailPendingCAS(ledger, "contract/admin_session_retire_deployment", "pinned", errors.New(message)); err != nil {
			t.Fatal(err)
		}
		r, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: "admin_session_retire_deployment", Key: "rejected-v1"}})
		if err != nil || r.IsError {
			t.Fatalf("probe: %+v/%v", r, err)
		}
		raw, _ := json.Marshal(r.StructuredContent)
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		want := "UNKNOWN"
		if marked {
			want = "rejected_before_execution"
		}
		if out["outcome"] != want || out["retry_allowed"] != false {
			t.Fatalf("unsafe probe: %s", raw)
		}
	}
}

func TestRetirementPreflightPublicWrapperBindsOwnerAndPayload(t *testing.T) {
	for _, mode := range []string{"fresh", "matching", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _ := yieldFixture(t)
			client := uploadClient(t, m, "holder", "operator")
			ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "holder", Role: "operator", Scopes: []string{"*"}})
			in := AdminSessionRetireInput{SessionID: strings.Repeat("a", 32), DeploymentID: "1792159092900-1e327387d562", ExpectedReceiptSHA256: strings.Repeat("b", 64), ExpectedCurrentSHA256: strings.Repeat("c", 64), Reason: strings.Repeat("x", 201), IdempotencyKey: "public-retirement"}
			payload, err := object(in)
			if err != nil {
				t.Fatal(err)
			}
			key := mutationReceiptKey(ctx, "admin_session_retire_deployment", in.IdempotencyKey, payload)
			sum := sha256.Sum256([]byte(key))
			ledger := "contract:" + hex.EncodeToString(sum[:])
			payload["_admin_session_owner"] = "holder"
			if mode == "foreign" {
				payload["_admin_session_owner"] = "other"
			}
			fp, err := idempotency.Fingerprint("admin_session_retire_deployment", payload)
			if err != nil {
				t.Fatal(err)
			}
			store := idempotency.NewWithDatabase(t.TempDir(), m.Database())
			if mode != "fresh" {
				if _, err := store.Begin(ledger, "contract/admin_session_retire_deployment", fp); err != nil {
					t.Fatal(err)
				}
			}
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "admin_session_retire_deployment", Arguments: in})
			if err != nil || !result.IsError {
				t.Fatalf("invalid request reached handler: %+v/%v", result, err)
			}
			status, err := store.Lookup(ledger)
			if mode == "fresh" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid request allocated receipt: %+v/%v", status, err)
				}
				return
			}
			want := idempotency.Failed
			if mode == "foreign" {
				want = idempotency.Pending
			}
			if err != nil || status.State != want || status.Fingerprint != fp {
				t.Fatalf("owner/payload protection failed: %+v/%v", status, err)
			}
		})
	}
}
