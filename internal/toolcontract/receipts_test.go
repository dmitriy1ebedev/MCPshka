package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"mcpshka/internal/idempotency"
)

func TestMutationReceiptExposesOnlySafeHandles(t *testing.T) {
	testMutationReceiptExposesOnlySafeHandles(t, false)
}

func TestMutationRefusalReceiptExposesOnlySafeHandles(t *testing.T) {
	testMutationReceiptExposesOnlySafeHandles(t, true)
}

func testMutationReceiptExposesOnlySafeHandles(t *testing.T, refused bool) {
	store := idempotency.New(t.TempDir())
	sum := sha256.Sum256([]byte("receipt-key"))
	key := "contract:" + hex.EncodeToString(sum[:])
	if _, err := store.Begin(key, "contract/fs_write", "fingerprint"); err != nil {
		t.Fatal(err)
	}
	receipt := Receipt[map[string]any]{IsError: refused, ErrorText: "secret-diagnostic", Output: map[string]any{"id": "safe-id", "lease_token": "secret-token", "contents": "secret-bytes", "job": map[string]any{"id": "safe-job"}}, Before: Evidence{State: "observed"}, After: Evidence{State: "observed"}}
	if err := store.Complete(key, "contract/fs_write", "fingerprint", receipt); err != nil {
		t.Fatal(err)
	}
	out, err := ProbeMutation(context.Background(), store, MutationProbeInput{Tool: "fs_write", Key: "receipt-key"}, ProbeOptions{ReceiptKey: "receipt-key"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	expected := "handler_completed"
	if refused {
		expected = "handler_reported_error"
	}
	if strings.Contains(string(raw), "secret-") || out["outcome"] != expected || out["retry_allowed"] != false || refused && out["handler_is_error"] != true {
		t.Fatalf("unsafe receipt: %s", raw)
	}
	handles, ok := out["handles"].(map[string]any)
	if !ok || len(handles) != 2 || handles["id"] != "safe-id" || handles["job_id"] != "safe-job" {
		t.Fatalf("lost handles: %s", raw)
	}
}

func TestMutationProbeRecognizesPrivateAtomicLeaseWithoutCredentialLeak(t *testing.T) {
	store := idempotency.New(t.TempDir())
	const receiptKey = "scoped-owner-key"
	sum := sha256.Sum256([]byte(receiptKey))
	key := "contract:" + hex.EncodeToString(sum[:])
	if _, err := store.Begin(key, "contract/project_lease_acquire", "outer-fp"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin("scoped-inner", "project_lease_acquire_atomic/v1", "inner-fp"); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("scoped-inner", "project_lease_acquire_atomic/v1", "inner-fp", map[string]any{"token": "private-credential-never-exposed", "status": map[string]any{"held": true}}); err != nil {
		t.Fatal(err)
	}
	out, err := ProbeMutation(context.Background(), store, MutationProbeInput{Tool: "project_lease_acquire", Key: "original-key"}, ProbeOptions{ReceiptKey: receiptKey, HandlerKey: "scoped-inner", HandlerOperation: "project_lease_acquire_atomic/v1", PrincipalScoped: true})
	if err != nil || out["handler_receipt"] != "completed" || out["retry_allowed"] != false {
		t.Fatal("lost scoped atomic receipt", out, err)
	}
	raw, err := json.Marshal(out)
	if err != nil || strings.Contains(string(raw), "private-credential") {
		t.Fatal("lease credential leaked through read-only probe", err)
	}
}
