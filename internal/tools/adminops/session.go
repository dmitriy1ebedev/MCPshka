package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/notify"
	"mcpshka/internal/ownerweb"
	"mcpshka/internal/statestore"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/roottask"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/rootbroker"
)

type AdminSessionInput struct {
	SessionID string `json:"session_id,omitempty"`
}
type AdminSessionBeginInput struct {
	AgentConsent   *rootbroker.AgentConsent `json:"agent_consent,omitempty" jsonschema:"Only on the owner's direct request: exact repository (path, plus up to four more in paths) and agent names to show and approve through Face ID. Omit for native_only (no additional agents). Never infer consent from quotas or a general task."`
	SessionID      string                   `json:"session_id"`
	Purpose        string                   `json:"purpose,omitempty" jsonschema:"What this Face ID is for, shown on the approval page, e.g. 'Влить 0.1.155 в main: 3 коммита, CI 7/7'. Up to 300 characters."`
	IdempotencyKey string                   `json:"idempotency_key"`
}
type AdminSessionEndInput struct {
	SessionID      string `json:"session_id"`
	Action         string `json:"action" jsonschema:"close or revoke"`
	IdempotencyKey string `json:"idempotency_key"`
}
type AdminSessionTaskInput struct {
	SessionID      string `json:"session_id"`
	TaskID         string `json:"task_id"`
	LiveSHA        string `json:"live_sha"`
	StagedSHA      string `json:"staged_sha"`
	IdempotencyKey string `json:"idempotency_key"`
}
type AdminSessionRetireInput struct {
	SessionID             string `json:"session_id"`
	DeploymentID          string `json:"deployment_id"`
	ExpectedReceiptSHA256 string `json:"expected_receipt_sha256"`
	ExpectedCurrentSHA256 string `json:"expected_current_sha256"`
	Reason                string `json:"reason" jsonschema:"Required 1..200 bytes; no CR, LF, NUL or leading dash. Fixed retirement protocol v1."`
	IdempotencyKey        string `json:"idempotency_key"`
}

type AdminLegacyLeasePreviewInput struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}
type AdminLegacyLeaseRecoverInput struct {
	Path           string                          `json:"path"`
	SessionID      string                          `json:"session_id"`
	Plan           projectlease.RootSealedRecovery `json:"plan"`
	IdempotencyKey string                          `json:"idempotency_key"`
}

type AdminSessionBeginResult struct {
	SessionID          string `json:"session_id"`
	Policy             string `json:"policy"`
	State              string `json:"state"`
	URL                string `json:"url,omitempty"`
	ChallengeExpiresAt string `json:"challenge_expires_at,omitempty"`
}

func Owner(ctx context.Context) (string, error) {
	identity, ok := authn.IdentityFromContext(ctx)
	if !ok || identity.ID == "" || identity.ID == "local-stdio" {
		return "", errors.New("authenticated administrative owner required")
	}
	return identity.ID, nil
}

func Run[T any](ctx context.Context, args ...string) (T, error) {
	var out T
	raw, err := brokerclient.Run(ctx, "admin-session", args...)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, errors.New("invalid administrative broker response")
	}
	return out, nil
}

func Register(add func(tooldef.Def), files *filesystem.Service, db *statestore.DB) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "admin_session_prepare", Description: "Prepare one owner-bound administrative Face ID session for related tasks. The work grant has no mandatory clock expiry; explicit close/revoke ends it. Existing operation allowlists, hash pins and release gates still apply.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, AdminSessionBeginResult, error) {
		if _, err := Owner(ctx); err != nil {
			return nil, AdminSessionBeginResult{}, err
		}
		protocol, err := Run[struct {
			Protocol int    `json:"protocol"`
			Policy   string `json:"policy"`
		}](ctx, "inspect")
		if err != nil {
			return nil, AdminSessionBeginResult{}, err
		}
		if protocol.Protocol != 1 || protocol.Policy != rootbroker.AdminSessionPolicy {
			return nil, AdminSessionBeginResult{}, errors.New("compatible administrative broker must be installed")
		}
		id, err := roottask.NewTaskID()
		return nil, AdminSessionBeginResult{SessionID: id, Policy: protocol.Policy, State: "prepared"}, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "admin_session_status", Description: "Inspect the authenticated owner's administrative session or pending challenge. A missing session_id reads the current grant. Does not return credentials or challenge data.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminSessionInput) (*mcp.CallToolResult, rootbroker.AdminSessionView, error) {
		owner, err := Owner(ctx)
		if err != nil {
			return nil, rootbroker.AdminSessionView{}, err
		}
		out, err := Run[rootbroker.AdminSessionView](ctx, "status", in.SessionID, owner)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "admin_session_begin", Description: "Create a purpose-specific Face ID link for one administrative work session. Default native_only forbids additional AI agents. Only on the owner's explicit delegation request provide agent_consent, displayed on the Face ID page. Reuses an active grant only with the same agent scope; changing scope requires closing it and fresh owner approval. Only the challenge expires after five minutes.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminSessionBeginInput) (*mcp.CallToolResult, AdminSessionBeginResult, error) {
		owner, err := Owner(ctx)
		if err != nil {
			return nil, AdminSessionBeginResult{}, err
		}
		prepared, err := Run[struct {
			PlanVersion int    `json:"plan_version"`
			TaskID      string `json:"task_id"`
			State       string `json:"state"`
			Scope       string `json:"scope"`
			Policy      string `json:"policy"`
			Origin      string `json:"origin"`
			ApprovalURL string `json:"approval_url"`
			ExpiresAt   string `json:"expires_at"`
		}](ctx, append([]string{"begin"}, BeginArgs(in.SessionID, owner, in.Purpose, in.AgentConsent)...)...)
		if err != nil {
			return nil, AdminSessionBeginResult{}, err
		}
		if prepared.PlanVersion != 2 || prepared.Policy != rootbroker.AdminSessionPolicy || prepared.Scope != "mcpshka_administration" || (prepared.State != "pending" && prepared.State != "active") {
			return nil, AdminSessionBeginResult{}, errors.New("untrusted administrative preparation")
		}
		out := AdminSessionBeginResult{SessionID: prepared.TaskID, Policy: prepared.Policy, State: prepared.State}
		if prepared.State == "pending" {
			origin, err := ownerweb.OriginConfigured()
			if err != nil || origin != prepared.Origin || prepared.TaskID != in.SessionID {
				return nil, out, errors.New("administrative approval origin or ID mismatch")
			}
			// Short link: phone terminals wrap the full ID and open only part of it.
			out.URL = prepared.ApprovalURL
			out.ChallengeExpiresAt = prepared.ExpiresAt
			if expiry, err := time.Parse(time.RFC3339Nano, prepared.ExpiresAt); err == nil {
				_ = notify.TrackAdmin(db, out.SessionID, owner, expiry)
			}
		}
		return nil, out, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "admin_session_end", Description: "Explicitly close or revoke the owner's administrative work session. Durable generation fencing prevents old tasks or old Face ID challenges from restoring authority. Inspect status after a lost response.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminSessionEndInput) (*mcp.CallToolResult, rootbroker.AdminSession, error) {
		owner, err := Owner(ctx)
		if err != nil {
			return nil, rootbroker.AdminSession{}, err
		}
		if in.Action != "close" && in.Action != "revoke" {
			return nil, rootbroker.AdminSession{}, errors.New("close or revoke required")
		}
		out, err := Run[rootbroker.AdminSession](ctx, in.Action, in.SessionID, owner)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "admin_session_authorize_task", Description: "Bind an exact prepared HA task and content hashes to the active administrative work session without another Face ID prompt. Execute, inspect or rollback with existing root_task tools.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminSessionTaskInput) (*mcp.CallToolResult, RootTaskResult, error) {
		owner, err := Owner(ctx)
		if err != nil {
			return nil, RootTaskResult{}, err
		}
		content, staged, err := roottask.GetStagedAutomations(files)
		if err != nil {
			return nil, RootTaskResult{}, err
		}
		if staged != in.StagedSHA {
			return nil, RootTaskResult{}, errors.New("staged SHA changed since preview")
		}
		parts, err := roottask.TaskContentChunks(content)
		if err != nil {
			return nil, RootTaskResult{}, err
		}
		args := append([]string{"authorize-task", in.SessionID, owner, in.TaskID, in.LiveSHA, in.StagedSHA}, parts...)
		out, err := Run[RootTaskResult](ctx, args...)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "project_lease_admin_recovery_status", Description: "Root-broker read-only current legacy lease holder observation under the active Face ID admin session; never discloses the current secret token.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminLegacyLeasePreviewInput) (*mcp.CallToolResult, projectlease.Status, error) {
		owner, err := Owner(ctx)
		if err != nil {
			return nil, projectlease.Status{}, err
		}
		out, err := Run[projectlease.Status](ctx, "status-legacy-lease", in.SessionID, owner, in.Path)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "project_lease_admin_recovery_preview", Description: "Prepare a pinned legacy lease recovery plan under an active owner-bound Face ID administrative session. Reports required root-sealed OS fencing and live process inspection. Read-only; no transfer.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminLegacyLeasePreviewInput) (*mcp.CallToolResult, projectlease.RootSealedRecoveryPreview, error) {
		owner, err := Owner(ctx)
		if err != nil {
			return nil, projectlease.RootSealedRecoveryPreview{}, err
		}
		out, err := Run[projectlease.RootSealedRecoveryPreview](ctx, "preview-legacy-lease", in.SessionID, owner, in.Path)
		return nil, out.WithState(), err
	}))
	add(tooldef.New(&mcp.Tool{Name: "project_lease_admin_recover", Description: "Recover one unbound legacy lease only with active Face ID admin session, SHA- and Git-tree-pinned plan, root-sealed repository/common-Git fence, no live process references, and atomic SQLite ownership rotation plus immutable audit journal. Fail closed; an UNKNOWN outcome must be inspected, never retried.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminLegacyLeaseRecoverInput) (*mcp.CallToolResult, projectlease.AcquireResult, error) {
		owner, err := Owner(ctx)
		if err != nil {
			return nil, projectlease.AcquireResult{}, err
		}
		if in.IdempotencyKey == "" || in.Path == "" || in.Path != in.Plan.Path {
			return nil, projectlease.AcquireResult{}, errors.New("pinned path and idempotency key required")
		}
		raw, err := json.Marshal(in.Plan)
		if err != nil {
			return nil, projectlease.AcquireResult{}, err
		}
		out, err := Run[projectlease.AcquireResult](ctx, "recover-legacy-lease", in.SessionID, owner, string(raw))
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "admin_session_retire_deployment", Description: "Permanently fence an inspected stale UNKNOWN deployment using the administrative work session and exact receipt/current hashes. Preserves original outcome and receipt; never retries deployment. Conflicting evidence remains HOLD.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminSessionRetireInput) (*mcp.CallToolResult, map[string]any, error) {
		owner, err := Owner(ctx)
		if err != nil {
			return nil, nil, err
		}
		out, err := Run[map[string]any](ctx, "retire-deployment", in.SessionID, owner, in.DeploymentID, in.ExpectedReceiptSHA256, in.ExpectedCurrentSHA256, in.Reason)
		return nil, out, err
	}))
}

func BeginArgs(id, owner, purpose string, consent ...*rootbroker.AgentConsent) []string {
	if len(consent) == 1 && consent[0] != nil {
		raw, _ := json.Marshal(consent[0])
		return []string{id, owner, purpose, string(raw)}
	}
	if purpose == "" {
		return []string{id, owner}
	}
	return []string{id, owner, purpose}
}

type RootTaskResult = roottask.RootTaskResult
