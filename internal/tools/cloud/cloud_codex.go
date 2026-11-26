package cloud

// Codex Cloud task metadata is untrusted evidence, not a local Codex session,
// a writer authorization, a verified source checkout, or permission to apply.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/tooldef"
)

var cloudCodexID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type CloudCodexListInput struct {
	SnapshotJSON        string `json:"snapshot_json" jsonschema:"Bounded JSON output of codex cloud list --json, supplied as UNTRUSTED evidence."`
	ExpectedEnvironment string `json:"expected_environment" jsonschema:"Explicit expected Codex Cloud environment ID; no automatic identity inference."`
	SourceDigest        string `json:"source_digest" jsonschema:"Pinned lowercase SHA-256 of candidate source; only caller supplied, never automatically verified."`
}

type cloudCodexWireTask struct {
	ID            string `json:"id"`
	EnvironmentID string `json:"environment_id"`
	Status        string `json:"status"`
	Title         string `json:"title"`
}

type CloudCodexTask struct {
	ID            string `json:"id"`
	EnvironmentID string `json:"environment_id"`
	Status        string `json:"status"`
	TitleSHA256   string `json:"title_sha256"`
}

type CloudCodexListResult struct {
	Schema           string           `json:"schema"`
	Provider         string           `json:"provider"`
	EnvironmentID    string           `json:"environment_id"`
	SourceDigest     string           `json:"source_digest"`
	Trust            string           `json:"trust"`
	SourceVerified   bool             `json:"source_verified"`
	TaskVerified     bool             `json:"task_verified"`
	DispatchEnabled  bool             `json:"dispatch_enabled"`
	DeployAuthorized bool             `json:"deploy_authorized"`
	CompletePage     bool             `json:"complete_page"`
	Tasks            []CloudCodexTask `json:"tasks"`
	NextSafeAction   string           `json:"next_safe_action"`
}

func normalizedCloudState(s string) string {
	switch s {
	case "queued", "pending", "in_progress", "completed", "failed", "cancelled":
		return s
	default:
		return "unknown"
	}
}

func PreviewCodex(in CloudCodexListInput) (CloudCodexListResult, error) {
	if len(in.SnapshotJSON) == 0 || len(in.SnapshotJSON) > 65536 || !cloudCodexID.MatchString(in.ExpectedEnvironment) ||
		!cloudDigestRE.MatchString(in.SourceDigest) {
		return CloudCodexListResult{}, errors.New("bounded snapshot, explicit environment and source digest required")
	}
	var envelope struct {
		Tasks  []cloudCodexWireTask `json:"tasks"`
		Cursor json.RawMessage      `json:"cursor"`
	}
	if !json.Valid([]byte(in.SnapshotJSON)) || json.Unmarshal([]byte(in.SnapshotJSON), &envelope) != nil ||
		envelope.Tasks == nil || len(envelope.Tasks) > 20 {
		return CloudCodexListResult{}, errors.New("invalid or oversized Codex Cloud task page")
	}
	var nextCursor string
	if len(envelope.Cursor) != 0 && string(envelope.Cursor) != "null" {
		if json.Unmarshal(envelope.Cursor, &nextCursor) != nil || len(nextCursor) > 1024 {
			return CloudCodexListResult{}, errors.New("invalid Codex Cloud pagination")
		}
	}
	out := CloudCodexListResult{
		Schema: "mcpshka.codex-cloud-observation/v1", Provider: "codex_cloud",
		EnvironmentID: in.ExpectedEnvironment, SourceDigest: in.SourceDigest,
		Trust: "UNTRUSTED", SourceVerified: false, TaskVerified: false,
		DispatchEnabled: false, DeployAuthorized: false, CompletePage: nextCursor == "",
		Tasks: []CloudCodexTask{}, NextSafeAction: "verify_authenticated_cloud_task_environment_source_and_owner_before_handoff",
	}
	seen := map[string]bool{}
	for _, task := range envelope.Tasks {
		if !cloudCodexID.MatchString(task.ID) || !cloudCodexID.MatchString(task.EnvironmentID) ||
			task.EnvironmentID != in.ExpectedEnvironment || seen[task.ID] || len(task.Title) > 4096 {
			return CloudCodexListResult{}, fmt.Errorf("invalid, duplicate or foreign Codex Cloud task")
		}
		seen[task.ID] = true
		h := sha256.Sum256([]byte(strings.TrimSpace(task.Title)))
		out.Tasks = append(out.Tasks, CloudCodexTask{
			ID: task.ID, EnvironmentID: task.EnvironmentID,
			Status: normalizedCloudState(task.Status), TitleSHA256: hex.EncodeToString(h[:]),
		})
	}
	return out, nil
}

func RegisterCodex(add func(tooldef.Def)) {
	closed := false
	add(tooldef.New(&mcp.Tool{
		Name:        "codex_cloud_observe",
		Description: "Parse a bounded codex cloud list --json task page into untrusted identity-only observations. Does NOT access Codex Cloud, read original chats, start agents, authorize worktrees, import diffs or promote releases.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CloudCodexListInput) (*mcp.CallToolResult, CloudCodexListResult, error) {
		out, err := PreviewCodex(in)
		return nil, out, err
	}))
}
