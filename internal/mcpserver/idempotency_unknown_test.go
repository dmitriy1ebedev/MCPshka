package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
)

func TestMutationProbeMissingAndPendingNeverAuthorizeRetry(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "pending"}[pending], func(t *testing.T) {
			m, _, _ := yieldFixture(t)
			client := uploadClient(t, m, "holder", "operator")
			if pending {
				sum := sha256.Sum256([]byte("probe-only"))
				s := idempotency.NewWithDatabase(t.TempDir(), m.Database())
				if _, err := s.Begin("contract:"+hex.EncodeToString(sum[:]), "contract/fs_patch", "pinned"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := m.Database().List("idempotency/")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "mutation_probe", Arguments: MutationProbeInput{Tool: "fs_patch", Key: "probe-only"},
			})
			if err != nil || result.IsError {
				t.Fatalf("probe: %+v %v", result, err)
			}
			raw, _ := json.Marshal(result.StructuredContent)
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			want := "not_found"
			if pending {
				want = "UNKNOWN"
			}
			if out["outcome"] != want || out["retry_allowed"] != false {
				t.Fatalf("unsafe probe: %s", raw)
			}
			after, err := m.Database().List("idempotency/")
			if err != nil || !slices.Equal(before, after) {
				t.Fatalf("probe changed durable receipts: %v", err)
			}
		})
	}
}

func TestMutationProbeForgeCIUsesAuthenticatedHandlerReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, owner, failure, want string
	}{
		{"own refusal", "holder\x00", "interactive or legacy project lease held: HOLD", "handler_failed"},
		{"foreign refusal", "other\x00", "interactive or legacy project lease held: HOLD", "UNKNOWN"},
		{"unscoped refusal", "", "interactive or legacy project lease held: HOLD", "UNKNOWN"},
		{"own pending", "holder\x00", "", "UNKNOWN"},
		{"own uncertain failure", "holder\x00", "remote effect unknown; reconcile", "UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := yieldFixture(t)
			client := uploadClient(t, m, "holder", "operator")
			s := idempotency.NewWithDatabase(t.TempDir(), m.Database())
			sum := sha256.Sum256([]byte("forge-01"))
			outer := "contract:" + hex.EncodeToString(sum[:])
			if _, err := s.Begin(outer, "contract/forge_ci", "pinned"); err != nil {
				t.Fatal(err)
			}
			inner := tc.owner + "forge-01"
			if _, err := s.Begin(inner, "forge_ci", "inner"); err != nil {
				t.Fatal(err)
			}
			if tc.failure != "" {
				if err := s.Fail(inner, "forge_ci", "inner", errors.New(tc.failure)); err != nil {
					t.Fatal(err)
				}
			}
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "mutation_probe", Arguments: MutationProbeInput{Tool: "forge_ci", Key: "forge-01"},
			})
			if err != nil || result.IsError {
				t.Fatalf("probe: %+v %v", result, err)
			}
			raw, _ := json.Marshal(result.StructuredContent)
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if out["outcome"] != tc.want || out["retry_allowed"] != (tc.want == "handler_failed") {
				t.Fatalf("unsafe recovery verdict: %s", raw)
			}
			if status, err := s.Lookup(outer); err != nil || status.State != idempotency.Pending {
				t.Fatalf("probe changed historical receipt: %+v %v", status, err)
			}
		})
	}
}

func TestMutationProbeReportsDefiniteHandlerFailure(t *testing.T) {
	for msg, want := range map[string]string{
		"open /root/repos/diag/.mcpshka/profile.json: no such file or directory": "handler_failed",
		"remote effect unknown; reconcile":                                       "UNKNOWN",
	} {
		m, _, _ := yieldFixture(t)
		client := uploadClient(t, m, "holder", "operator")
		sum := sha256.Sum256([]byte("write-01"))
		s := idempotency.NewWithDatabase(t.TempDir(), m.Database())
		if _, err := s.Begin("contract:"+hex.EncodeToString(sum[:]), "contract/fs_write", "pinned"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Begin("write-01", "fs_write", "inner"); err != nil {
			t.Fatal(err)
		}
		if err := s.Fail("write-01", "fs_write", "inner", errors.New(msg)); err != nil {
			t.Fatal(err)
		}
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "mutation_probe", Arguments: MutationProbeInput{Tool: "fs_write", Key: "write-01"},
		})
		if err != nil || result.IsError {
			t.Fatalf("probe: %+v %v", result, err)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out["outcome"] != want || out["retry_allowed"] != (want == "handler_failed") {
			t.Fatalf("%q: %s", msg, raw)
		}
	}
}
