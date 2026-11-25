// Package hatools exposes Home Assistant configuration reads and the
// Face ID-gated exposure/entity changes as MCP tools (moved out of
// mcpserver in 0.18.0, S5.LEAN).
package hatools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/haconfig"
	"mcpshka/internal/haprovider"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/tooldef"
)

type HAConfigReadInput struct {
	File string `json:"file" jsonschema:"configuration.yaml, automations.yaml, scripts.yaml, scenes.yaml, customize.yaml or groups.yaml"`
}

type HAInventoryInput struct {
	Query      string `json:"query,omitempty" jsonschema:"substring of entity_id, name, device or area"`
	ZigbeeOnly bool   `json:"zigbee_only,omitempty" jsonschema:"only Zigbee2MQTT devices"`
	Limit      int    `json:"limit,omitempty" jsonschema:"rows, default 200, max 500"`
}

// MCP-241: no key, IP, device secret, HA .storage read or options flow.
type HAConfigEntriesInput struct {
	Domain string `json:"domain,omitempty" jsonschema:"optional safe integration domain, such as meross_lan"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum entry metadata rows; default 100, max 200"`
}

type HARekeyPreflightInput struct {
	Domain  string `json:"domain" jsonschema:"exact HA integration domain, e.g. meross_lan"`
	EntryID string `json:"entry_id" jsonschema:"exact existing HA config entry ID; rekey is never started by this read"`
}

type HAInventoryResult struct {
	Rows      []haconfig.Row    `json:"rows"`
	Exports   []haconfig.Export `json:"exports"`
	Total     int               `json:"total"`
	Truncated bool              `json:"truncated"`
}

type HAExposureSyncInput struct {
	Action         string   `json:"action" jsonschema:"plan, apply or restore"`
	Targets        []string `json:"targets,omitempty" jsonschema:"export entry ids or domains (homekit, yandex_smart_home); default all"`
	Entities       []string `json:"entities,omitempty" jsonschema:"add exactly these entity ids (they must exist in HA) instead of every wanted Zigbee entity; plan and apply must pass the same list"`
	Digest         string   `json:"digest,omitempty" jsonschema:"apply: digest of the reviewed plan"`
	Backup         string   `json:"backup,omitempty" jsonschema:"restore: backup path from apply"`
	SessionID      string   `json:"admin_session_id,omitempty" jsonschema:"apply/restore: active Face ID session"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

type HAExposureSyncResult struct {
	Plan     *haconfig.SyncPlan    `json:"plan,omitempty"`
	Apply    *haconfig.ApplyResult `json:"apply,omitempty"`
	Restored map[string][]string   `json:"restored,omitempty"`
}

type HAEntityUpdateInput struct {
	haconfig.EntityChange
	SessionID      string `json:"admin_session_id" jsonschema:"active Face ID session"`
	IdempotencyKey string `json:"idempotency_key"`
}

type HAEntityUpdateResult struct {
	Before haconfig.EntityChange `json:"before"`
	After  haconfig.EntityChange `json:"after"`
}

// Register installs the Home Assistant configuration tools; requireAdmin
// admits a change only under the owner's active Face ID session.
func Register(add func(tooldef.Def), requireAdmin func(context.Context, string) error) {
	api := haprovider.New()
	add(tooldef.New(&mcp.Tool{Name: "ha_config_read", Description: "Read one Home Assistant YAML file (configuration, automations, scripts, scenes, customize, groups) through the root broker with inline passwords/tokens masked; secrets.yaml and .storage are never readable. Read-only.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in HAConfigReadInput) (*mcp.CallToolResult, rootbroker.HAConfigFile, error) {
		var out rootbroker.HAConfigFile
		raw, err := brokerclient.Run(ctx, "ha-config", "read", in.File)
		if err != nil {
			return nil, out, err
		}
		return nil, out, json.Unmarshal(raw, &out)
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "ha_config_entries",
		Description: "Read-only sanitized Home Assistant integration config-entry inventory. Filter by domain (e.g. meross_lan) to inspect existing entry IDs/states and detect duplicates before an owner-approved rekey. No title, host, key, pairing code, options, .storage or raw responses are exposed; no config/flow mutation.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HAConfigEntriesInput) (*mcp.CallToolResult, haconfig.ConfigEntryMetadataResult, error) {
		out, err := haconfig.ReadConfigEntryMetadata(ctx, api, in.Domain, in.Limit)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "ha_rekey_preflight",
		Description: "Read-only rekey readiness check for exactly one existing Home Assistant integration entry. HOLD on ambiguous domain, missing target, unknown state or already loaded entry; approval still required and no key, flow or integration config is read or changed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in HARekeyPreflightInput) (*mcp.CallToolResult, haconfig.ConfigEntryRekeyPreflight, error) {
		out, err := haconfig.ReadConfigEntryRekeyPreflight(ctx, api, in.Domain, in.EntryID)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "ha_inventory", Description: "Home Assistant inventory: each entity with its device, integration, area, Zigbee2MQTT origin, entity category and the export entries (HomeKit bridges, Yaha Cloud/Alice) that already include it, plus every export list. Reads registries over HA's WebSocket API and export options through their options flows (aborted, nothing saved). Read-only.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in HAInventoryInput) (*mcp.CallToolResult, HAInventoryResult, error) {
		inv, err := haconfig.Load(ctx, api)
		if err != nil {
			return nil, HAInventoryResult{}, err
		}
		limit := in.Limit
		if limit <= 0 || limit > 500 {
			limit = 200
		}
		q := strings.ToLower(in.Query)
		out := HAInventoryResult{Exports: inv.Exports, Rows: []haconfig.Row{}}
		for _, row := range inv.Rows {
			if (in.ZigbeeOnly && !row.Zigbee) || (q != "" && !strings.Contains(strings.ToLower(row.Entity+" "+row.Name+" "+row.Device+" "+row.Area), q)) {
				continue
			}
			out.Total++
			if len(out.Rows) < limit {
				out.Rows = append(out.Rows, row)
			}
		}
		out.Truncated = out.Total > len(out.Rows)
		return nil, out, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "ha_exposure_sync", Description: "Incremental export of Zigbee2MQTT devices to existing HomeKit bridges and Yaha Cloud (Alice): plan lists exactly what each target would gain (controllable devices and useful sensors; never config/diagnostic, disabled or hidden entities; existing entries untouched; a second run adds nothing) with a digest and switch/light warnings; apply (Face ID session + digest) backs up every list, saves through the integrations' own options flows, re-reads to verify and restores all on any failure; restore puts a backup back. Never creates bridges or touches other settings.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in HAExposureSyncInput) (*mcp.CallToolResult, HAExposureSyncResult, error) {
		var out HAExposureSyncResult
		switch in.Action {
		case "plan":
			inv, err := haconfig.Load(ctx, api)
			if err != nil {
				return nil, out, err
			}
			p, err := haconfig.Plan(inv, in.Targets, in.Entities)
			out.Plan = &p
			return nil, out, err
		case "apply", "restore":
			if err := requireAdmin(ctx, in.SessionID); err != nil {
				return nil, out, err
			}
			if in.Action == "restore" {
				var err error
				out.Restored, err = haconfig.Restore(ctx, api, in.Backup)
				return nil, out, err
			}
			res, err := haconfig.Apply(ctx, api, in.Digest, in.Targets, in.Entities)
			out.Apply = &res
			return nil, out, err
		}
		return nil, out, errors.New("action must be plan, apply or restore")
	}))
	add(tooldef.New(&mcp.Tool{Name: "ha_entity_update", Description: "Rename a Home Assistant entity or move it to an area without changing its entity_id (an empty value resets to the default); returns the previous values for rollback (call again with them) and verifies the registry. Requires the active Face ID session.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in HAEntityUpdateInput) (*mcp.CallToolResult, HAEntityUpdateResult, error) {
		if err := requireAdmin(ctx, in.SessionID); err != nil {
			return nil, HAEntityUpdateResult{}, err
		}
		before, err := haconfig.UpdateEntity(ctx, api, in.EntityChange)
		return nil, HAEntityUpdateResult{Before: before, After: in.EntityChange}, err
	}))
}
