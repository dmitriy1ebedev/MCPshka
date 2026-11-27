package roottask

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/tooldef"
)

const haStagingFile = "/var/lib/chatops/ha-bridge/automations.yaml"

// haStagingHint is the whole change path: the task replaces exactly
// automations.yaml with the staged file; nothing else in /config.
const haStagingHint = "NO_CHANGES: the staged file equals the live automations.yaml. Write the complete new automations.yaml (UTF-8, at most 10 KiB) to " + haStagingFile + " with fs_write or fs_patch, check it with ha_check_config, then call root_task_prepare again. Only automations.yaml can be replaced this way; configuration.yaml and other /config files have no task-scoped writer."

// The authenticated broker's JSON envelope is capped at 16 KiB. A 10 KiB
// YAML staging file fits after base64 encoding and argument metadata.
const MaxPayload = 10240

type RootTaskInput struct {
	TaskID string `json:"task_id" jsonschema:"32-character task identifier returned by root_task_prepare."`
}
type RootTaskAuthorizeInput struct {
	TaskID         string `json:"task_id"`
	Scope          string `json:"scope"`
	LiveSHA        string `json:"live_sha"`
	StagedSHA      string `json:"staged_sha"`
	Code           string `json:"code" jsonschema:"One 6-digit Apple Passwords TOTP code, never a root password or recovery secret."`
	IdempotencyKey string `json:"idempotency_key"`
}
type RootTaskPasskeyInput struct {
	TaskID         string `json:"task_id"`
	LiveSHA        string `json:"live_sha"`
	StagedSHA      string `json:"staged_sha"`
	IdempotencyKey string `json:"idempotency_key"`
}
type RootTaskPasskeyResult struct {
	PlanVersion int    `json:"plan_version,omitempty"`
	TaskID      string `json:"task_id"`
	Scope       string `json:"scope"`
	State       string `json:"state"`
	URL         string `json:"url"`
	ExpiresAt   string `json:"expires_at"`
	LiveSHA     string `json:"live_sha"`
	StagedSHA   string `json:"staged_sha"`
}
type RootTaskExecuteInput struct {
	TaskID         string `json:"task_id"`
	Action         string `json:"action" jsonschema:"apply, rollback, or close the approved task; no arbitrary shell commands."`
	IdempotencyKey string `json:"idempotency_key"`
}
type RootTaskResult struct {
	AdminSessionID     string `json:"admin_session_id,omitempty"`
	AdminGeneration    int64  `json:"admin_generation,omitempty"`
	AuthorizationState string `json:"authorization_state,omitempty"`
	AuthMethod         string `json:"auth_method,omitempty"`
	AuthorizedAt       string `json:"authorized_at,omitempty"`
	PlanVersion        int    `json:"plan_version,omitempty"`
	TaskID             string `json:"task_id,omitempty"`
	Scope              string `json:"scope"`
	State              string `json:"state"`
	LiveSHA            string `json:"live_sha,omitempty"`
	StagedSHA          string `json:"staged_sha,omitempty"`
	ExpiresAt          string `json:"expires_at,omitempty"`
	BackupCreated      bool   `json:"backup_created,omitempty"`
	ReloadRequired     bool   `json:"reload_required,omitempty"`
	Instructions       string `json:"instructions,omitempty"`
}

func decodeRootTask(ctx context.Context, args ...string) (RootTaskResult, error) {
	raw, e := brokerclient.Run(ctx, "root-task", args...)
	if e != nil {
		return RootTaskResult{}, e
	}
	var out RootTaskResult
	if e = json.Unmarshal(raw, &out); e != nil {
		return RootTaskResult{}, errors.New("invalid privileged broker response")
	}
	return out, nil
}
func requireRootTaskProtocol(ctx context.Context) error {
	state, err := decodeRootTask(ctx, "inspect")
	if err != nil {
		return err
	}
	if state.PlanVersion != 1 {
		return errors.New("privileged broker requires approval plan version 1; install the compatible helper")
	}
	return nil
}
func NewTaskID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func GetStagedAutomations(files *filesystem.Service) ([]byte, string, error) {
	doc, e := files.Read(haStagingFile, 0, MaxPayload, false)
	if e != nil {
		return nil, "", e
	}
	if doc.Encoding != "utf-8" || doc.Truncated || doc.TotalBytes <= 0 || doc.TotalBytes > MaxPayload {
		return nil, "", errors.New("staged HA YAML must be UTF-8 and at most 10 KiB")
	}
	b := []byte(doc.Text)
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}

func TaskContentChunks(content []byte) ([]string, error) {
	if len(content) == 0 || len(content) > MaxPayload {
		return nil, errors.New("automation content exceeds broker payload bound")
	}
	encoded := base64.StdEncoding.EncodeToString(content)
	var parts []string
	for len(encoded) > 0 {
		n := len(encoded)
		if n > 2800 {
			n = 2800
		}
		parts = append(parts, encoded[:n])
		encoded = encoded[n:]
	}
	return parts, nil
}

type Deps struct {
	Files *filesystem.Service
	Owner func(context.Context) string
}

func Register(add func(tooldef.Def), d Deps) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "root_task_prepare", Description: "Prepare a task-scoped privileged Home Assistant automation update: replaces automations.yaml with the file staged at /var/lib/chatops/ha-bridge/automations.yaml (write it first with fs_write; equal hashes return state NO_CHANGES). Reports the exact current/staged SHA-256 and task ID for Face ID confirmation; existing TOTP authorization remains a fallback. Preview needs no code or root password.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, RootTaskResult, error) {
		var out RootTaskResult
		_, stage, e := GetStagedAutomations(d.Files)
		if e != nil {
			return nil, out, e
		}
		out, e = decodeRootTask(ctx, "inspect")
		if e != nil {
			return nil, out, e
		}
		if out.PlanVersion != 1 {
			return nil, out, errors.New("privileged broker requires approval plan version 1")
		}
		if out.LiveSHA != "" && out.LiveSHA == stage {
			// 27.10: an agent prepared a task whose staged file was the live
			// one and could not find where the change goes.
			out.StagedSHA, out.State = stage, "NO_CHANGES"
			out.Instructions = haStagingHint
			return nil, out, nil
		}
		id, e := NewTaskID()
		if e != nil {
			return nil, RootTaskResult{}, e
		}
		out.TaskID = id
		out.StagedSHA = stage
		out.Instructions = "Review the Home Assistant automation replacement and pinned hashes, then call root_task_begin_passkey for the owner Face ID link. The broker applies the confirmed task automatically; observe root_task_status without another user command. Authorized task steps include rollback and close. Existing TOTP authorization remains available; never share its enrollment secret."
		return nil, out, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "root_task_status", Description: "Read the current task-scoped root authorization state, expiration and pinned content hashes, without disclosing secrets.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in RootTaskInput) (*mcp.CallToolResult, RootTaskResult, error) {
		out, e := decodeRootTask(ctx, "status", in.TaskID, d.Owner(ctx))
		return nil, out, e
	}))
	add(tooldef.New(&mcp.Tool{Name: "root_task_begin_passkey", Description: "Prepare a five-minute, SHA-pinned WebAuthn Face ID confirmation link. No TOTP, root password or full-root shell. Requires an enrolled passkey and configured HTTPS origin.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in RootTaskPasskeyInput) (*mcp.CallToolResult, RootTaskPasskeyResult, error) {
		var out RootTaskPasskeyResult
		_, staged, e := GetStagedAutomations(d.Files)
		if e != nil {
			return nil, out, e
		}
		if staged != in.StagedSHA {
			return nil, out, errors.New("staged SHA changed since preview")
		}
		if err := requireRootTaskProtocol(ctx); err != nil {
			return nil, out, err
		}
		raw, e := brokerclient.Run(ctx, "passkey", "begin", in.TaskID, d.Owner(ctx), in.LiveSHA, in.StagedSHA)
		if e != nil {
			return nil, out, e
		}
		var prepared struct {
			PlanVersion int    `json:"plan_version"`
			TaskID      string `json:"task_id"`
			State       string `json:"state"`
			Origin      string `json:"origin"`
			ApprovalURL string `json:"approval_url"`
			Scope       string `json:"scope"`
			LiveSHA     string `json:"live_sha"`
			StagedSHA   string `json:"staged_sha"`
			ExpiresAt   string `json:"expires_at"`
		}
		if e = json.Unmarshal(raw, &prepared); e != nil {
			return nil, out, e
		}
		if prepared.PlanVersion != 1 || prepared.State != "pending" || prepared.TaskID != in.TaskID {
			return nil, out, errors.New("untrusted broker preparation")
		}
		out = RootTaskPasskeyResult{PlanVersion: prepared.PlanVersion, TaskID: prepared.TaskID, Scope: prepared.Scope, State: prepared.State, URL: prepared.ApprovalURL, LiveSHA: prepared.LiveSHA, StagedSHA: prepared.StagedSHA, ExpiresAt: prepared.ExpiresAt}
		return nil, out, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "root_task_authorize", Description: "Authorize one entire approved HA automation task with a single fresh Apple Passwords TOTP code. The code is consumed by the privileged broker, never stored in MCPshka receipts; all subsequent allowed task steps use task_id without repeating a code. Does NOT unlock an unrestricted root shell.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in RootTaskAuthorizeInput) (*mcp.CallToolResult, RootTaskResult, error) {
		if len(in.Code) != 6 {
			return nil, RootTaskResult{}, errors.New("six digit TOTP code required")
		}
		if in.Scope != "homeassistant_automations" {
			return nil, RootTaskResult{}, errors.New("unsupported task scope")
		}
		_, stage, e := GetStagedAutomations(d.Files)
		if e != nil {
			return nil, RootTaskResult{}, e
		}
		if stage != in.StagedSHA {
			return nil, RootTaskResult{}, errors.New("staged SHA changed since preview")
		}
		if err := requireRootTaskProtocol(ctx); err != nil {
			return nil, RootTaskResult{}, err
		}
		out, e := decodeRootTask(ctx, "authorize", in.TaskID, in.Scope, in.LiveSHA, in.StagedSHA, d.Owner(ctx), in.Code)
		return nil, out, e
	}))
	add(tooldef.New(&mcp.Tool{Name: "root_task_execute", Description: "Perform a fixed allowlisted step of an owner-approved root task (Face ID or TOTP; HA automations: apply, rollback, or close). No new OTP is needed while the task authorization remains active. SHA-guarded apply makes a root-owned backup. Does not permit arbitrary commands.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in RootTaskExecuteInput) (*mcp.CallToolResult, RootTaskResult, error) {
		if in.Action != "apply" && in.Action != "rollback" && in.Action != "close" {
			return nil, RootTaskResult{}, errors.New("unsupported task action")
		}
		if err := requireRootTaskProtocol(ctx); err != nil {
			return nil, RootTaskResult{}, err
		}
		argv := []string{"execute", in.Action, in.TaskID, d.Owner(ctx)}
		if in.Action == "apply" {
			content, _, e := GetStagedAutomations(d.Files)
			if e != nil {
				return nil, RootTaskResult{}, e
			}
			parts, e := TaskContentChunks(content)
			if e != nil {
				return nil, RootTaskResult{}, e
			}
			argv = append(argv, parts...)
		}
		out, e := decodeRootTask(ctx, argv...)
		return nil, out, e
	}))
}
