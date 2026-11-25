package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/backupreport"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"strings"
	"time"
)

type StateBackupInput struct {
	ContractApprovalToken string `json:"contract_approval_token,omitempty"`
	IdempotencyKey        string `json:"idempotency_key"`
}
type StateBackupIDInput struct {
	BackupID string `json:"backup_id"`
}

type StateRestoreInput struct {
	BackupID       string `json:"backup_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func stateBackupCall(ctx context.Context, action, id string) (map[string]any, error) {
	if len(id) != 32 {
		return nil, errors.New("backup_id must be 32 hex characters")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return nil, err
	}
	raw, err := brokerclient.Run(ctx, "state-backup", action, id)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}
func registerStateTools(registry *toolRegistry, m *jobs.Manager, store *idempotency.Store) {
	closed, nonDestructive := false, false
	addTool(registry, &mcp.Tool{Name: "state_status", Description: "Compact authoritative state mode and record count; never returns payloads or secrets.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, map[string]any, error) {
		db := m.Database()
		if db == nil {
			return nil, map[string]any{"mode": "legacy"}, nil
		}
		count, capacity, err := db.CountWithCapacity()
		return nil, map[string]any{"mode": "sqlite-v1", "records": count, "capacity": capacity}, err
	})
	addTool(registry, &mcp.Tool{Name: "state_backup", Description: "Start a bounded encrypted off-node state/artifact backup to the pinned private Drive folder. Probe before retry; worker verifies download, decrypt and clean restore. No arbitrary destination or key.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in StateBackupInput) (*mcp.CallToolResult, map[string]any, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, nil, errors.New("retry key required")
		}
		sum := sha256.Sum256([]byte(in.IdempotencyKey))
		id := hex.EncodeToString(sum[:16])
		out, err := runIdempotent(store, in.IdempotencyKey, "state_backup", in, func() (map[string]any, error) {
			out, err := stateBackupCall(ctx, "start", id)
			if err != nil {
				return nil, codexUnknownError{err}
			}
			return out, nil
		})
		return nil, out, err
	})
	addTool(registry, &mcp.Tool{Name: "state_backup_status", Description: "Probe a durable backup receipt including remote ID, checksum and verified restoration. No archive contents or credentials.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in StateBackupIDInput) (*mcp.CallToolResult, map[string]any, error) {
		out, err := stateBackupCall(ctx, "probe", in.BackupID)
		if err == nil {
			recovery, readErr := backupreport.Read(ctx, m.Database(), time.Now().UTC())
			if readErr != nil {
				return nil, out, readErr
			}
			out["recovery"] = recovery
		}
		return nil, out, err
	})
	addTool(registry, &mcp.Tool{Name: "state_restore_verify", Description: "Download/decrypt a previously verified backup into a fresh isolated temporary destination, validate SQLite records, then clean up. Never replaces live state. A required retry key identifies one isolated verification attempt; probe state_backup_status.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in StateRestoreInput) (*mcp.CallToolResult, map[string]any, error) {
		out, err := runIdempotent(store, in.IdempotencyKey, "state_restore_verify", in, func() (map[string]any, error) { return stateBackupCall(ctx, "restore", in.BackupID) })
		return nil, out, err
	})
}
