package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/tooldef"
)

type IntegrationTestPrepareInput struct {
	SessionID string `json:"admin_session_id" jsonschema:"Active Face ID administrative session that covers this task."`
	Path      string `json:"path" jsonschema:"Canonical repository root, for example /root/repos/mcpshka."`
	Commit    string `json:"commit" jsonschema:"Full commit SHA of the integration candidate."`
	Attempt   int    `json:"attempt,omitempty" jsonschema:"Run number 1-9 for this commit (default 1). A permit runs once; use the next attempt to test the same commit again."`
	Evidence  string `json:"evidence,omitempty" jsonschema:"distributed: root re-fetches shard observations, verifies snapshot/tree/coverage, and seals all shards; H96 fallback is limited to missing shards. ci (default): root seals GitHub CI results of this exact commit, nothing is built on the host. gitverse: root recomputes the immutable GitVerse snapshot of the commit and seals its passing release-suite and snapshot-contracts jobs (second CI when GitHub minutes run out; publish the snapshot first with packaging/gitverse/snapshot_immutable.py). module: offline go test ./... in the host sandbox (fallback when CI is unavailable; heavy)."`
	// Preparation is idempotent per repository and commit; the key only
	// satisfies the mutation contract's replay ledger.
	IdempotencyKey string `json:"idempotency_key"`
}

// ReleaseBuildPrepareInput has no evidence: the build is sealed alone and
// the test permit is passed to release_local (sharing the test input type
// advertised an evidence argument the tool then refused, BUG-20261023-65).
type ReleaseBuildPrepareInput struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID administrative session that covers this task."`
	Path           string `json:"path" jsonschema:"Canonical repository root, for example /root/repos/mcpshka."`
	Commit         string `json:"commit" jsonschema:"Full commit SHA already published to main."`
	Attempt        int    `json:"attempt,omitempty" jsonschema:"Build number 1-9 for this commit (default 1)."`
	IdempotencyKey string `json:"idempotency_key"`
}

type IntegrationApproveInput struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID administrative session that covers this task."`
	Path           string `json:"path" jsonschema:"Repository root of the pending integration item."`
	ID             string `json:"id" jsonschema:"Pending integration item ID."`
	StoreID        string `json:"store_id" jsonschema:"Protected repository store ID (32 hex)."`
	PermitID       string `json:"permit_id" jsonschema:"Fenced successful evidence permit (CI or whole-module test) for the item's commit."`
	IdempotencyKey string `json:"idempotency_key"`
}

// Both actions need the owner's live Face ID grant; root re-checks Git
// identities and test evidence itself. Neither runs Git effects or deploys.
func (svc Service) RegisterAdmin(add func(tooldef.Def), e *integrationqueue.Runtime) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "integration_test_prepare", Description: "Seal test evidence for an integration candidate commit under the active Face ID session. evidence=ci (default): root reads GitHub check runs of the exact commit (all required jobs success, PR base an ancestor) and seals them; the launch only re-verifies, nothing builds on the host. evidence=module: offline go test ./... in the host sandbox (fallback, heavy). Asynchronous and idempotent per commit and kind: returns QUEUED with reason and position, PREPARING, FAILED with reason and next_action, or the SEALED manifest with permit/launch IDs; poll with a new retry key (a repeated key replays the first reply). Launch, observe and fence it with exec_start command=mcpshka args=[executor-canary, launch|status|canary-fence, permit, launch].", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IntegrationTestPrepareInput) (*mcp.CallToolResult, map[string]any, error) {
		owner, err := svc.AdminOwner(ctx)
		if err != nil {
			return nil, nil, err
		}
		action := "integration-ci-prepare"
		switch in.Evidence {
		case "", "ci":
		case "distributed":
			action = "integration-distributed-prepare"
		case "gitverse":
			action = "integration-gitverse-prepare"
		case "module":
			action = "integration-test-prepare"
		default:
			return nil, nil, errors.New("evidence must be ci, gitverse, distributed or module")
		}
		args := []string{action, in.SessionID, owner, in.Path, in.Commit}
		if in.Attempt > 1 {
			if in.Attempt > 9 {
				return nil, nil, errors.New("attempt must be 1-9")
			}
			args = append(args, strconv.Itoa(in.Attempt))
		}
		out, err := svc.Admin(ctx, args...)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "integration_approve", Description: "Issue the root-sealed protected integration approval for a pending item under the active Face ID session. Root verifies the live branch and target heads, the fenced successful evidence (sealed GitHub CI or whole-module test) of the exact commit and the protected store; the reviewer is the Face ID owner and the review digest covers the target..candidate diff. Then use integration_worker_launch.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in IntegrationApproveInput) (*mcp.CallToolResult, map[string]any, error) {
		owner, err := svc.AdminOwner(ctx)
		if err != nil {
			return nil, nil, err
		}
		status, err := e.Status(ctx, in.Path, in.ID)
		if err != nil {
			return nil, nil, err
		}
		if !status.Found || status.Item == nil || status.Item.State != integrationqueue.Pending {
			return nil, nil, errors.New("pending integration item not found: HOLD")
		}
		item, err := json.Marshal(status.Item)
		if err != nil {
			return nil, nil, err
		}
		out, err := svc.Admin(ctx, "integration-approve", in.SessionID, owner, in.StoreID, in.PermitID, string(item))
		return nil, out, err
	}))
}
