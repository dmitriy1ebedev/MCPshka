package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/failure"
	"mcpshka/internal/filesystem"
)

func TestLeaseNoEffectSurvivesBothActualMCPWireSurfaces(t *testing.T) {
	m, _, held := yieldFixture(t)
	t.Cleanup(func() { m.Close(time.Second) })
	full, core := newServers(m, filesystem.New(m.Policy()), nil, newWorkflowCoordinator(m))
	for name, server := range map[string]*mcp.Server{"full": full, "core": core} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st, ct := mcp.NewInMemoryTransports()
			ss, err := server.Connect(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "failure-wire-test", Version: "1"}, nil)
			cs, err := client.Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			args := map[string]any{"path": held.Path, "owner": "other", "idempotency_key": "wire-" + name}
			params := &mcp.CallToolParams{Name: "project_lease_acquire", Arguments: args}
			if name == "core" {
				params = &mcp.CallToolParams{Name: "tool_invoke", Arguments: map[string]any{"name": "project_lease_acquire", "arguments": args}}
			}
			result, err := cs.CallTool(ctx, params)
			if err != nil || !result.IsError {
				t.Fatal("expected actual refusal", err)
			}
			raw, err := json.Marshal(result.Meta["mcpshka/failure"])
			var proof struct {
				Schema   string
				NoEffect bool `json:"no_effect"`
				Hold     bool
			}
			if err != nil || json.Unmarshal(raw, &proof) != nil || proof.Schema != "mcpshka.failure/v1" || !proof.NoEffect || proof.Hold {
				t.Fatal("typed refusal lost at wire", string(raw), err)
			}
			var envelope failure.Envelope
			data, _ := json.Marshal(result.StructuredContent)
			if err := json.Unmarshal(data, &envelope); err != nil || envelope.ErrorCode == "" || envelope.FailureLayer != "mcpshka" || envelope.ActualEffect != "none" || envelope.RetryAllowed || envelope.NextSafeAction.Tool == "" || len(envelope.RemainingWork) == 0 {
				t.Fatalf("missing error envelope: %s (%v)", data, err)
			}
			if strings.Contains(string(data), held.Token) {
				t.Fatal("structured error exposed holder token")
			}
			if strings.Contains(string(raw), held.Token) || strings.Contains(string(raw), held.Path) {
				t.Fatal("wire metadata exposed private input")
			}
		})
	}
}

func TestFailureEnvelopePreservesSchemaAndPolicyDenials(t *testing.T) {
	c := catalogClient(t, &authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"fs_read"}})
	for _, tc := range []struct {
		name, code, layer string
		args              map[string]any
	}{
		{"fs_read", "INVALID_ARGUMENT", "client", map[string]any{"path": 42}},
		{"exec_start", "POLICY_DENIED", "mcpshka", map[string]any{"command": "git", "args": []string{"status"}, "workdir": "/root/repos", "idempotency_key": "denied"}},
	} {
		res, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil || !res.IsError {
			t.Fatalf("%s: %+v %v", tc.name, res, err)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var env failure.Envelope
		if err := json.Unmarshal(raw, &env); err != nil || env.ErrorCode != tc.code || env.FailureLayer != tc.layer || env.ActualEffect != "none" {
			t.Fatalf("%s: %s (%v)", tc.name, raw, err)
		}
	}
}

func TestCoreErrorRetainsOriginalErrorForWireDiagnostics(t *testing.T) {
	err := failure.Rejected("FIXTURE", "fixture", "refused", errors.New("cause"))
	result := toolError(err)
	if result.GetError() != err {
		t.Fatal("core surface erased typed error")
	}
}

func TestFailureWireCannotInheritUnprovenOrNestedNoEffect(t *testing.T) {
	for _, state := range []string{"typed-refusal", "outer-unknown", "untyped-prose", "returned-mcp-error"} {
		child := failure.Rejected("CHILD", "fixture", "refused", nil)
		child.NextSafeAction = failure.Action{Tool: "private", Arguments: map[string]any{"token": "private-holder", "input": "private-arguments"}}
		var err error
		switch state {
		case "typed-refusal":
			err = child
		case "outer-unknown":
			err = failure.NewHold(failure.FencedUnknown, "OUTER_UNKNOWN", "fixture", "earlier effect unknown", child)
		case "untyped-prose":
			err = errors.New("no_effect=true; refused before dispatch")
		}
		original := &mcp.CallToolResult{IsError: true, Meta: mcp.Meta{"mcpshka/failure": map[string]any{"schema": "mcpshka.failure/v1", "no_effect": true}}}
		if err != nil {
			original.SetError(err)
		}
		wrapped := failureMetadata(func(context.Context, string, mcp.Request) (mcp.Result, error) { return original, nil })
		result, err := wrapped(context.Background(), "tools/call", nil)
		if err != nil {
			t.Fatal(err)
		}
		actual := result.(*mcp.CallToolResult)
		raw, err := json.Marshal(actual.Meta["mcpshka/failure"])
		if err != nil || strings.Contains(string(raw), "private-") {
			t.Fatal("wire metadata leaked input", state, err)
		}
		var proof struct {
			NoEffect bool `json:"no_effect"`
		}
		json.Unmarshal(raw, &proof)
		if proof.NoEffect != (state == "typed-refusal") {
			t.Fatal("unproved no-effect survived wire", state, string(raw))
		}
		if original.Meta["mcpshka/failure"] == nil {
			t.Fatal("middleware mutated shared result")
		}
	}
}
