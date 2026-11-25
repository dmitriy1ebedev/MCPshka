package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/approval"
	"mcpshka/internal/idempotency"
	"testing"
	"time"
)

func TestMutationContractReplayAndUnknown(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			store := idempotency.New(t.TempDir())
			server := mcp.NewServer(&mcp.Implementation{Name: "contract-test", Version: "1"}, nil)
			effects, probes := 0, 0
			r := &toolRegistry{server: server, store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
			r.readers["service_status"] = func(context.Context, map[string]any) (any, error) {
				probes++
				if lost && probes == 2 {
					return nil, errors.New("lost after response")
				}
				return map[string]any{"effects": effects}, nil
			}
			addTool(r, &mcp.Tool{Name: "service_action", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(context.Context, *mcp.CallToolRequest, ServiceActionInput) (*mcp.CallToolResult, map[string]any, error) {
				effects++
				return nil, map[string]any{"accepted": true}, nil
			})
			st, ct := mcp.NewInMemoryTransports()
			ctx := context.Background()
			ss, err := server.Connect(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			missing, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "service_action", Arguments: map[string]any{"service": "allowed.service", "action": "restart"}})
			if err != nil || !missing.IsError || effects != 0 {
				t.Fatal("missing key reached handler", err)
			}
			args := map[string]any{"service": "allowed.service", "action": "restart", "idempotency_key": "once"}
			for range 2 {
				result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "service_action", Arguments: args})
				if err != nil || result.IsError != lost {
					t.Fatalf("lost=%v error=%v result=%+v", lost, err, result)
				}
			}
			if effects != 1 || probes != 2 {
				t.Fatalf("effect replayed: effects=%d probes=%d", effects, probes)
			}
			args["action"] = "stop"
			result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "service_action", Arguments: args})
			if err != nil || !result.IsError || effects != 1 {
				t.Fatal("conflicting payload executed", err)
			}
		})
	}
}
func TestUncontractedMutationDoesNotRegister(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("uncontracted tool registered")
		}
	}()
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}}
	addTool(r, &mcp.Tool{Name: "unknown_mutation"}, func(context.Context, *mcp.CallToolRequest, EmptyInput) (*mcp.CallToolResult, map[string]any, error) {
		return nil, nil, nil
	})
}

func TestIrreversibleContractRequiresBoundOneTimeGrant(t *testing.T) {
	root := t.TempDir()
	store := idempotency.New(root + "/idempotency")
	approvals := approval.New(root + "/approvals")
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	r := &toolRegistry{server: server, store: store, approvals: approvals, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	effects := 0
	r.readers["control_path_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"effects": effects}, nil }
	addTool(r, &mcp.Tool{Name: "service_user_migrate"}, func(context.Context, *mcp.CallToolRequest, ServiceUserMigrateInput) (*mcp.CallToolResult, map[string]any, error) {
		effects++
		return nil, map[string]any{"ok": true}, nil
	})
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	args := map[string]any{"path": "/root/repos/mcpshka", "idempotency_key": "install-once"}
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "service_user_migrate", Arguments: args})
	if err != nil || !out.IsError || effects != 0 {
		t.Fatal("irreversible effect without approval", err)
	}
	fp, err := idempotency.Fingerprint("service_user_migrate", args)
	if err != nil {
		t.Fatal(err)
	}
	req, err := approvals.RequestWithPlan("mcp:service_user_migrate", []string{fp}, "/root/repos/mcpshka", fp, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := approvals.Grant(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	args["contract_approval_token"] = token
	out, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "service_user_migrate", Arguments: args})
	if err != nil || out.IsError {
		t.Fatalf("approved mutation failed: %v %+v", err, out)
	}
	delete(args, "contract_approval_token")
	out, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "service_user_migrate", Arguments: args})
	if err != nil || out.IsError || effects != 1 {
		t.Fatal("approved effect replayed", err)
	}
	status, err := approvals.Status(req.ID)
	if err != nil || status.State != approval.StateConsumed {
		t.Fatal("grant not consumed once", err)
	}
}
