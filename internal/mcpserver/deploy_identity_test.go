package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
)

func TestDeploymentReservationIsStableAndNativeGateCompatible(t *testing.T) {
	receipt := idempotency.Status{Operation: reservedDeployOperation, StartedAt: time.UnixMilli(1792159092900), Fingerprint: strings.Repeat("a", 64)}
	id, err := reservedDeploymentID("once", receipt)
	if err != nil || !regexp.MustCompile(`^[0-9]{13}-[a-f0-9]{12}$`).MatchString(id) {
		t.Fatalf("identity=%s err=%v", id, err)
	}
	again, err := reservedDeploymentID(" once ", receipt)
	if err != nil || again != id {
		t.Fatal("canonical retry key changed reservation")
	}
	other, _ := reservedDeploymentID("other", receipt)
	if other == id {
		t.Fatal("different retry key reused identity")
	}
	receipt.Operation = "project_deploy"
	if _, err := reservedDeploymentID("once", receipt); err == nil {
		t.Fatal("guessed legacy random deployment ID")
	}
}

func TestMutationProbeRecoversDeploymentAfterLostReplyAndControllerRestart(t *testing.T) {
	for _, operation := range []string{reservedDeployOperation, "project_deploy"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			key := "lost-deploy-reply"
			store := idempotency.New(dir)
			fp, err := idempotency.Fingerprint(operation, map[string]string{"candidate": "pinned"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Begin(key, operation, fp); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(key))
			outerKey := "contract:" + hex.EncodeToString(sum[:])
			if _, err := store.Begin(outerKey, "contract/project_deploy", "exact-outer-fingerprint"); err != nil {
				t.Fatal(err)
			}
			inner, _ := store.Lookup(key)
			reserved, _ := reservedDeploymentID(key, inner)
			for range 2 {
				// Reopen the existing receipt after loss of the launch response. Helper is already terminal.
				reopened := idempotency.New(dir)
				server := mcp.NewServer(&mcp.Implementation{Name: "cold-probe", Version: "1"}, nil)
				r := &toolRegistry{server: server, store: reopened, readers: map[string]probeHandler{}, tools: map[string]bool{}}
				probes := 0
				r.readers["control_path_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil }
				r.readers["project_deploy_status"] = func(_ context.Context, in map[string]any) (any, error) {
					probes++
					if in["deployment_id"] != reserved {
						t.Fatalf("probe addressed wrong deployment: %v", in)
					}
					return map[string]any{"deployment_id": reserved, "state": "succeeded"}, nil
				}
				registerContractTools(r)
				st, ct := mcp.NewInMemoryTransports()
				ctx := context.Background()
				ss, err := server.Connect(ctx, st, nil)
				if err != nil {
					t.Fatal(err)
				}
				cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
				if err != nil {
					t.Fatal(err)
				}
				result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: "project_deploy", Key: key, Input: map[string]any{"path": "/root/repos/mcpshka"}}})
				if err != nil || result.IsError {
					t.Fatalf("cold probe=%+v err=%v", result, err)
				}
				raw, _ := json.Marshal(result.StructuredContent)
				var out map[string]any
				if err = json.Unmarshal(raw, &out); err != nil {
					t.Fatal(err)
				}
				if out["retry_allowed"] != false || out["outcome"] != "UNKNOWN" {
					t.Fatalf("probe granted replay: %s", raw)
				}
				if operation == reservedDeployOperation {
					handles, ok := out["handles"].(map[string]any)
					if !ok || handles["deployment_id"] != reserved || probes != 1 {
						t.Fatalf("lost deployment handle: %s", raw)
					}
				} else if out["handles"] != nil || probes != 0 {
					t.Fatalf("fabricated legacy correlation: %s", raw)
				}
				status, err := reopened.Lookup(key)
				if err != nil || status.State != idempotency.Pending {
					t.Fatal("probe rewrote PENDING")
				}
				cs.Close()
				ss.Close()
			}
		})
	}
}
