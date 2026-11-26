package hostops

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/hostconfig"
	"mcpshka/internal/systeminfo"
	"mcpshka/internal/tooldef"
)

func RegisterHost(add func(tooldef.Def), d Deps) {
	manager := d.Manager
	makeHostInventory := d.HostInventory
	closed := false

	// MCP-250: the filesystem API correctly refuses arbitrary /etc reads.
	// This typed Samba inspector replaces the RDC read-only testparm bypass
	// with fixed-path, redacted evidence and no shell/config mutation.
	add(tooldef.New(&mcp.Tool{
		Name:        "smb_share_status",
		Description: "Read-only bounded /etc/samba/smb.conf share/security projection. Returns recognized shares, fixed known paths, observed readonly/admin-user risk and the presence of bind and host rules. No arbitrary paths, usernames, secrets, passwords, credential database or raw config exposed; no service mutation.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, hostconfig.SambaReport, error) {
		if !brokerclient.Enabled() {
			return nil, hostconfig.InspectSamba(), nil
		}
		var out hostconfig.SambaReport
		raw, err := brokerclient.Run(ctx, "smb-share-status")
		if err != nil {
			return nil, out, err
		}
		return nil, out, json.Unmarshal(raw, &out)
	}))

	// MCP-247/248/249: read installed Caddy Face ID and its import state
	// natively. No arbitrary paths, shell commands, raw secrets or writes.
	add(tooldef.New(&mcp.Tool{
		Name:        "host_config_inspect",
		Description: "Read-only structured Caddy Face ID/OAuth host-config inventory: exact fixed /etc/caddy files only, symlink/size guards, site binds, route parity, masked unexpected directives, SHA256 and import state. Never returns raw configuration or credentials; does not probe live HTTPS.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, hostconfig.Report, error) {
		out, err := hostconfig.InspectCaddy()
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "host_storage_inventory",
		Description: "Host-wide cached top directories, growth trends and time to disk reserve/full, plus bounded project storage and Git/writer evidence. Inaccessible production, mounts, processes and jobs are explicitly UNKNOWN; never interpreted as disposable.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HostStorageInventoryInput) (*mcp.CallToolResult, systeminfo.HostStorageInventory, error) {
		out, err := makeHostInventory(ctx, in.Path)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "host_housekeeping_plan",
		Description: "Conservative KEEP/REVIEW storage plan with exact zero verified reclaim until process, Docker mounts, jobs, leases and pinned production/rollback evidence are available. Never deletes files.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HostStorageInventoryInput) (*mcp.CallToolResult, systeminfo.HostHousekeepingPlan, error) {
		inventory, err := makeHostInventory(ctx, in.Path)
		if err != nil {
			return nil, systeminfo.HostHousekeepingPlan{}, err
		}
		return nil, systeminfo.PlanHostHousekeeping(inventory), nil
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "host_relocate_plan",
		Description: "Read-only evidence and dependency review for a proposed filesystem relocation. Returns HOLD until fixed source identity, systemd, Docker, Git worktrees, Python environments, jobs and rollback are all verified. Does not move data.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HostRelocatePlanInput) (*mcp.CallToolResult, systeminfo.HostRelocatePlan, error) {
		out, err := systeminfo.PlanHostRelocate(ctx, manager.Policy(), in.Source, in.Destination)
		return nil, out, err
	}))
}
