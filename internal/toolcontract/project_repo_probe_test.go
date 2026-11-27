package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"mcpshka/internal/idempotency"
)

func TestProjectRepoAdoptProbeArguments(t *testing.T) {
	for _, source := range []string{"", "/root/project-to-move"} {
		for _, before := range []bool{true, false} {
			in := map[string]any{
				"admin_session_id": "authorized-session",
				"path":             "/root/repos/probe-fixture",
				"plan_sha256":      "pinned-plan",
				"idempotency_key":  "mutation-key",
			}
			want := map[string]any{"admin_session_id": in["admin_session_id"], "path": in["path"]}
			if source != "" {
				in["source"] = source
				if before {
					want["source"] = source
				}
			}
			// Neither the result nor mutation-only fields may replace the pinned preview inputs.
			out := map[string]any{"path": "/root/repos/other", "admin_session_id": "other-session", "source": "/root/other"}
			probe, args, err := ProbeArguments("project_repo_adopt", Contracts["project_repo_adopt"].Probe, in, out, before)
			if err != nil || probe != "project_repo_preview" || !reflect.DeepEqual(args, want) {
				t.Fatalf("source=%q before=%v: probe=%q args=%v err=%v; want %v", source, before, probe, args, err, want)
			}
		}
	}
}

func TestProjectRepoAdoptPendingReceiptStaysUnknown(t *testing.T) {
	store := idempotency.New(t.TempDir())
	const retryKey = "adopt-held-before-probe"
	sum := sha256.Sum256([]byte(retryKey))
	ledgerKey := "contract:" + hex.EncodeToString(sum[:])
	if _, err := store.Begin(ledgerKey, "contract/project_repo_adopt", "pinned-fingerprint"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := ProbeMutation(context.Background(), store, MutationProbeInput{Tool: "project_repo_adopt", Key: retryKey}, ProbeOptions{ReceiptKey: retryKey})
		if err != nil || out["outcome"] != "UNKNOWN" || out["retry_allowed"] != false {
			t.Fatalf("pending receipt became replayable: %v, %v", out, err)
		}
		status, err := store.Lookup(ledgerKey)
		if err != nil || status.State != idempotency.Pending {
			t.Fatalf("read-only reconciliation changed receipt: %v, %v", status, err)
		}
	}
}
