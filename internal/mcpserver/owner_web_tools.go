package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"mcpshka/internal/authn"
	"mcpshka/internal/ownerweb"
)

// ownerWebToolCall lets the Face ID owner pages call tools through the full
// wrapper as identity owner-web, scoped to exactly ownerweb.WebTools plus the
// lease calls «Код» makes on the owner's behalf.
func ownerWebToolCall(r *toolRegistry) func(context.Context, string, map[string]any) (json.RawMessage, error) {
	scopes := append([]string{"project_lease_acquire", "project_lease_heartbeat", "project_lease_release"}, ownerweb.WebTools...)
	identity := authn.Identity{ID: "owner-web", Source: "faceid-session", Role: "operator", Scopes: scopes}
	return func(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
		if args != nil {
			if _, ok := args["idempotency_key"]; !ok && r.lookupTool(name).tool != nil && !r.lookupTool(name).readOnly {
				args["idempotency_key"] = "owner-web-" + ownerweb.RandomHex(16)
			}
		}
		out, err := r.invoke(authn.WithIdentity(ctx, identity), ToolInvokeInput{Name: name, Arguments: args})
		if err != nil {
			return nil, err
		}
		if out.IsError {
			return nil, errors.New(strings.Join(out.Content, "; "))
		}
		return json.Marshal(out.Output)
	}
}
