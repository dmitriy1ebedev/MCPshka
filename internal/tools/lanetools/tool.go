package lanetools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/tooldef"
)

func Register(add func(tooldef.Def), store *idempotency.Store) {
	add(tooldef.New(&mcp.Tool{Name: "lane_status", Description: "Read Lane status without Face ID; also the before/after observation for lane recipes.", OutputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, json.RawMessage, error) {
		out, err := brokerclient.Run(ctx, "lane", `{"action":"status"}`)
		return nil, json.RawMessage(out), err
	}))
	add(tooldef.New(&mcp.Tool{Name: "lane", Description: "Lane orchestration: status, engines status, router status/wifi/clients/history/plan, device list, diag export (since) without Face ID; router status/wifi/clients/history, device list and diag export use Lane's native read-only socket, the broker is fallback only for absent socket. Mutations require Face ID: router apply with plan before_sha256, engines update, dns apply/commit, service mode. Fixed broker recipes; raw Lane JSON with Lane's secret masking. BE900_SESSION_BUSY means wait for the current router session.", OutputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest, in Input) (*mcp.CallToolResult, json.RawMessage, error) {
		ctx = tooldef.InvocationContext(ctx, req)
		ctx, cancel := context.WithTimeout(ctx, 17*time.Minute)
		defer cancel()
		_, _, mutation, err := recipe(in)
		if err != nil {
			return nil, nil, err
		}
		wire := in
		wire.SessionID, wire.IdempotencyKey = "", ""
		raw, err := json.Marshal(wire)
		if err != nil {
			return nil, nil, err
		}
		if len(raw) > 4096 {
			return nil, nil, errors.New("LANE_RECIPE_TOO_LARGE: broker accepts up to 4096 bytes")
		}
		if !mutation {
			if out, err := nativeRead(ctx, in); !errors.Is(err, errNoNative) && !errors.Is(err, laneprovider.ErrReaderUnavailable) {
				return nil, out, err
			}
			out, err := brokerclient.Run(ctx, "lane", string(raw))
			return nil, json.RawMessage(out), err
		}
		identity, ok := authn.IdentityFromContext(ctx)
		if !ok || identity.ID == "" || identity.ID == "local-stdio" || in.SessionID == "" || in.IdempotencyKey == "" {
			return nil, nil, errors.New("LANE_FACE_ID_REQUIRED: authenticated owner, admin_session_id and idempotency_key required")
		}
		digest := sha256.Sum256(raw)
		payload := struct{ Session, Owner, SHA256 string }{in.SessionID, identity.ID, hex.EncodeToString(digest[:])}
		out, err := idempotency.Run(store, in.IdempotencyKey, "lane", payload, func() (json.RawMessage, error) {
			out, err := brokerclient.Run(ctx, "admin-session", "lane", in.SessionID, identity.ID, string(raw))
			return json.RawMessage(out), err
		})
		return nil, out, err
	}))
}

var errNoNative = errors.New("no native Lane read for this action")

var reader = laneprovider.NewReader()
var routerReader = laneprovider.NewRouterReader()

// nativeReadOperation never admits router plans, snapshots or writes.
// These four BE900 commands are read-only and share Lane's native reader gate.
func nativeReadOperation(action string) (string, bool) {
	switch action {
	case "device list":
		return "lane.device.list", true
	case "diag export":
		return "lane.diag.export", true
	case "router status":
		return "lane.router.status", true
	case "router wifi":
		return "lane.router.wifi", true
	case "router clients":
		return "lane.router.clients", true
	case "router history":
		return "lane.router.history", true
	default:
		return "", false
	}
}

// nativeRead prefers Lane's read-only socket (lane-api.service), independent
// from retired laned.service. Broker fallback is only for missing sockets;
// a responder error is preserved and NEVER converted to a write attempt.
func nativeRead(ctx context.Context, in Input) (json.RawMessage, error) {
	operation, ok := nativeReadOperation(in.Action)
	if !ok {
		return nil, errNoNative
	}
	if in.Action == "diag export" {
		since := in.Since
		if since == "" {
			since = "24h"
		}
		return reader.Read(ctx, operation, map[string]any{"since": since})
	}
	if len(operation) >= len("lane.router.") && operation[:len("lane.router.")] == "lane.router." {
		return routerReader.Read(ctx, operation, nil)
	}
	return reader.Read(ctx, operation, nil)
}
