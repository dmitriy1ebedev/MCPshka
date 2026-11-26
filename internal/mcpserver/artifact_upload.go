package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/authn"
	"mcpshka/internal/jobs"
)

type ArtifactUploadInput struct {
	ID             string `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	Size           int64  `json:"size,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Offset         int64  `json:"offset,omitempty"`
	DataBase64     string `json:"data_base64,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}
type ArtifactUploadStatusInput struct {
	ID string `json:"id"`
}

var uploadOwner = authn.Owner

func runArtifactUpload(ctx context.Context, inbox *artifacts.Inbox, m *jobs.Manager, action string, in ArtifactUploadInput) (artifacts.Upload, error) {
	var zero artifacts.Upload
	if action != "status" {
		if _, e := m.Policy().DecideCommand("mcpshka-artifact-upload", nil); e != nil {
			return zero, e
		}
	}
	owner := uploadOwner(ctx)
	switch action {
	case "begin":
		return inbox.Begin(owner, in.IdempotencyKey, in.Name, in.Size, in.SHA256)
	case "status":
		return inbox.Status(owner, in.ID)
	case "chunk":
		if len(in.DataBase64) > base64.StdEncoding.EncodedLen(artifacts.MaxUploadChunk) {
			return zero, errors.New("artifact chunk exceeds 24KiB")
		}
		data, e := base64.StdEncoding.Strict().DecodeString(in.DataBase64)
		if e != nil || base64.StdEncoding.EncodeToString(data) != in.DataBase64 {
			return zero, errors.New("invalid canonical base64 chunk")
		}
		return inbox.Chunk(owner, in.ID, in.Offset, data)
	case "finish":
		return inbox.Finish(owner, in.ID)
	case "cancel":
		return inbox.Cancel(owner, in.ID)
	default:
		return zero, errors.New("artifact-upload action requires begin/chunk/status/finish/cancel")
	}
}
func registerArtifactUploads(r *toolRegistry, m *jobs.Manager, inbox *artifacts.Inbox) {
	closed := false
	addTool(r, &mcp.Tool{Name: "artifact_upload_status", Description: "Probe the authenticated principal's durable artifact inbox entry and confirmed byte offset. No file bytes or automatic import; UNTRUSTED evidence. reconcile_required means probe receipts before resuming.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ArtifactUploadStatusInput) (*mcp.CallToolResult, artifacts.Upload, error) {
		out, e := inbox.Status(uploadOwner(ctx), in.ID)
		return nil, out, e
	})
	for _, action := range []string{"begin", "chunk", "finish", "cancel"} {
		addTool(r, &mcp.Tool{Name: "artifact_upload_" + action, Description: "Receive a file through authenticated MCP without URLs, shell or caller-selected destination. begin requires name/size/SHA-256; chunk requires id/offset/canonical base64 (24KiB max); finish verifies full SHA-256 into an UNTRUSTED inbox. 16MiB/file, 64MiB inbox, 24h incomplete TTL; cancellation deletes only incomplete owned blobs. No automatic apply or execution. Requires retry key.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ArtifactUploadInput) (*mcp.CallToolResult, artifacts.Upload, error) {
			out, e := runArtifactUpload(ctx, inbox, m, action, in)
			return nil, out, e
		})
	}
}
func artifactUploadCompat(ctx context.Context, m *jobs.Manager, inbox *artifacts.Inbox, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Args) != 3 || len(in.Args[2]) > 40*1024 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("artifact-upload requires ACTION JSON and no execution overrides")
	}
	if _, e := m.Policy().CheckWorkdir(in.Workdir); e != nil {
		return jobs.Snapshot{}, e
	}
	var input ArtifactUploadInput
	if e := json.Unmarshal([]byte(in.Args[2]), &input); e != nil {
		return jobs.Snapshot{}, e
	}
	if input.IdempotencyKey != "" && input.IdempotencyKey != in.IdempotencyKey {
		return jobs.Snapshot{}, errors.New("upload retry key differs from exec_start key")
	}
	input.IdempotencyKey = in.IdempotencyKey
	out, e := runArtifactUpload(ctx, inbox, m, in.Args[1], input)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	hash := sha256.Sum256([]byte(in.Args[2]))
	ownerHash := sha256.Sum256([]byte(uploadOwner(ctx)))
	return m.StoreReceipt("mcpshka", []string{"artifact-upload", in.Args[1], "request-sha256:" + hex.EncodeToString(hash[:]), "owner-sha256:" + hex.EncodeToString(ownerHash[:])}, in.Workdir, string(raw))
}
func artifactInbox(m *jobs.Manager) *artifacts.Inbox {
	return artifacts.NewInbox(filepath.Join(filepath.Dir(m.StateDir()), "artifact-inbox"), m.Database())
}

func isArtifactUploadMutation(name string, input map[string]any) bool {
	if strings.HasPrefix(name, "artifact_upload_") {
		return true
	}
	if name != "exec_start" {
		return false
	}
	command, _ := input["command"].(string)
	args, _ := input["args"].([]any)
	return (command == "mcpshka" || command == "/usr/local/bin/mcpshka") && len(args) > 0 && args[0] == "artifact-upload"
}

func uploadReceiptKey(ctx context.Context, name, key string, input map[string]any) string {
	if isArtifactUploadMutation(name, input) {
		return uploadOwner(ctx) + "\x00" + key
	}
	return key
}
func authorizeUploadReceipt(ctx context.Context, m *jobs.Manager, id string) error {
	snapshot, e := m.Get(id)
	if e != nil {
		return e
	}
	if snapshot.Command != "mcpshka" || len(snapshot.Args) == 0 || snapshot.Args[0] != "artifact-upload" {
		return nil
	}
	h := sha256.Sum256([]byte(uploadOwner(ctx)))
	if len(snapshot.Args) != 4 || snapshot.Args[3] != "owner-sha256:"+hex.EncodeToString(h[:]) {
		return errors.New("artifact receipt owner mismatch")
	}
	return nil
}
func validateCachedUpload(ctx context.Context, r *toolRegistry, name string, input, cached map[string]any) error {
	if !isArtifactUploadMutation(name, input) {
		return nil
	}
	if name == "exec_start" {
		id, _ := cached["id"].(string)
		result, e := r.lookupTool("job_output").reader(ctx, map[string]any{"job_id": id, "limit": 4096})
		if e != nil {
			return e
		}
		value, e := object(result)
		if e != nil {
			return e
		}
		text, _ := value["text"].(string)
		if e = json.Unmarshal([]byte(text), &cached); e != nil {
			return e
		}
	}
	id, _ := cached["id"].(string)
	result, e := r.lookupTool("artifact_upload_status").reader(ctx, map[string]any{"id": id})
	if e != nil {
		return e
	}
	current, e := object(result)
	if e != nil {
		return e
	}
	if cached["state"] == "ready" && current["state"] != "ready" {
		return errors.New("cached artifact no longer ready")
	}
	if current["state"] == "cancelled" && cached["state"] != "cancelled" {
		return errors.New("cached artifact was cancelled")
	}
	return nil
}
