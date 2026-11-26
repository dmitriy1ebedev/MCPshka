package mcpserver

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/tools/capabilities"
)

type CapabilityDenied = capabilities.CapabilityDenied

type CapabilityInventoryInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type CapabilityInventory struct {
	Denied      []CapabilityDenied `json:"denied"`
	DeniedCount int                `json:"denied_count"`
	NextCursor  string             `json:"next_cursor,omitempty"`
}

func (r *toolRegistry) capabilityDenial(ctx context.Context, t *mcp.Tool) *CapabilityDenied {
	d := &CapabilityDenied{Name: t.Name, ReasonCode: "SCOPE_NOT_GRANTED", RequiredPermission: t.Name, NextSafeAction: "ask the owner to grant only this tool's required scope; do not bypass policy"}
	if err := authorizeTool(ctx, t); err != nil {
		if id, ok := authn.IdentityFromContext(ctx); ok {
			role := strings.ToLower(strings.TrimSpace(id.Role))
			if role != "" && role != "operator" && role != "reader" || role == "reader" && (t.Annotations == nil || !t.Annotations.ReadOnlyHint) {
				d.ReasonCode = "ROLE_NOT_ALLOWED"
				d.NextSafeAction = "ask the owner to review the required role and scope; a route cannot expand authority"
			}
		}
		return d
	}
	if t.Name == "broker_install" && (brokerclient.Enabled() || os.Geteuid() != 0) {
		d.ReasonCode = "BOOTSTRAP_ONLY"
		d.NextSafeAction = "broker installation is unavailable in this service; inspect broker health, do not reinstall through another route"
		return d
	}
	return nil
}
func (r *toolRegistry) deniedInventory(ctx context.Context, in CapabilityInventoryInput) (CapabilityInventory, error) {
	return r.deniedInventorySnapshot(ctx, in, r.snapshotRegistry())
}

func (r *toolRegistry) deniedInventorySnapshot(ctx context.Context, in CapabilityInventoryInput, view registrySnapshot) (CapabilityInventory, error) {
	out := CapabilityInventory{Denied: []CapabilityDenied{}}
	if len(in.Cursor) > 128 || in.Limit < 0 || in.Limit > 32 {
		return out, errors.New("inventory cursor <=128 bytes and limit 0..32 required")
	}
	if in.Limit == 0 {
		in.Limit = 16
	}
	for _, name := range view.names() {
		d := r.capabilityDenial(ctx, view.catalog[name])
		// Bootstrap-only is not an authorization gap the owner can grant: clients showed it as «1 not authorized».
		if d == nil || d.ReasonCode == "BOOTSTRAP_ONLY" {
			continue
		}
		out.DeniedCount++
		if name <= in.Cursor {
			continue
		}
		if len(out.Denied) < in.Limit {
			out.Denied = append(out.Denied, *d)
		} else if out.NextCursor == "" {
			out.NextCursor = out.Denied[len(out.Denied)-1].Name
		}
	}
	return out, nil
}
func registerCapabilityInventory(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "capability_inventory", Description: "Page through unauthorized tool names, reason codes, narrow required permissions and safe next actions. No denied schemas, secrets or execution authority.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in CapabilityInventoryInput) (*mcp.CallToolResult, CapabilityInventory, error) {
		out, err := r.deniedInventory(ctx, in)
		return nil, out, err
	})
}
