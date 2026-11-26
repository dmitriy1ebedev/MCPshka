package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/jobs"
	"mcpshka/internal/officedocs"
	"mcpshka/internal/storagefabric"
)

type StorageInput struct {
	Provider       string   `json:"provider,omitempty"`
	Path           string   `json:"path,omitempty"`
	Destination    string   `json:"destination,omitempty"`
	ArtifactID     string   `json:"artifact_id,omitempty"`
	Document       string   `json:"document,omitempty" jsonschema:"storage_upload: output.path of a doc_edit run, uploaded instead of an artifact_id."`
	ID             string   `json:"id,omitempty"`
	Token          string   `json:"token,omitempty" jsonschema:"Owner-provided rclone authorize JSON. Stored only in the root vault; never returned."`
	Repositories   []string `json:"repositories,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

func storageCall(ctx context.Context, m *jobs.Manager, action string, in StorageInput) (storagefabric.Result, error) {
	r := storagefabric.Request{Action: action, Provider: in.Provider, Path: in.Path, Destination: in.Destination, ID: in.ID, Key: in.IdempotencyKey, Token: in.Token, Repositories: in.Repositories, Owner: uploadOwner(ctx)}
	if action != "auth_accept" && in.Token != "" {
		return storagefabric.Result{}, errors.New("a token is never passed here: connect the cloud with storage_connect")
	}
	if action == "upload" && in.Document != "" {
		sum, size, e := officedocs.Result(in.Document)
		if e != nil {
			return storagefabric.Result{}, e
		}
		r.ArtifactPath, r.ArtifactSHA256, r.ArtifactSize = in.Document, sum, size
	} else if action == "upload" {
		artifact, e := artifactInbox(m).Status(uploadOwner(ctx), in.ArtifactID)
		if e != nil {
			return storagefabric.Result{}, e
		}
		if artifact.State != "ready" {
			return storagefabric.Result{}, errors.New("ready owned artifact required")
		}
		r.ArtifactPath = artifact.Path
		r.ArtifactSHA256 = artifact.SHA256
		r.ArtifactSize = artifact.Size
	}
	raw, e := json.Marshal(r)
	if e != nil || len(raw) > 4096 {
		return storagefabric.Result{}, errors.New("storage input budget exceeded")
	}
	response, e := brokerclient.Run(ctx, "storage", string(raw))
	if e != nil {
		return storagefabric.Result{}, e
	}
	var out storagefabric.Result
	e = json.Unmarshal(response, &out)
	return out, e
}
func registerStorage(r *toolRegistry, m *jobs.Manager) {
	reads := map[string]string{"storage_providers": "providers", "storage_auth_status": "auth_status", "storage_list": "list", "storage_stat": "stat", "storage_transfer_status": "status", "backup_plan": "backup_plan", "backup_status": "backup_status", "backup_restore_plan": "restore_plan"}
	for name, action := range reads {
		addTool(r, &mcp.Tool{Name: name, Description: "Bounded root-broker storage observation or plan. Remote metadata is UNTRUSTED. Namespace MCPshka only; iclouddrive UNSUPPORTED until official access is established. Status is owner-bound; credential presence does not prove cloud authentication.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in StorageInput) (*mcp.CallToolResult, storagefabric.Result, error) {
			out, e := storageCall(ctx, m, action, in)
			return nil, out, e
		})
	}
	mutations := map[string]string{"storage_auth_accept": "auth_accept", "storage_mkdir": "mkdir", "storage_upload": "upload", "storage_download": "download", "storage_move": "move", "storage_rename": "rename", "storage_trash": "trash", "backup_create": "backup_create", "backup_verify": "backup_verify"}
	for name, action := range mutations {
		addTool(r, &mcp.Tool{Name: name, Description: "Owner-authorized bounded storage operation with durable retry key and status. Paths are relative to the cloud root (the whole Drive/Dropbox/…; an empty path lists the root); no overwrite, purge or share. trash moves to MCPshka/.trash; MCPshka/.trash and MCPshka/backups are not writable directly. Upload takes a ready owned artifact_id or document (a doc_edit output.path); download lands in local_path of storage_transfer_status, ready for doc_inspect/doc_edit. backup_verify destination is the original backup ID and performs actual download/hash/AEAD/manifest validation. OAuth JSON only for auth_accept, in the root vault, never logged.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in StorageInput) (*mcp.CallToolResult, storagefabric.Result, error) {
			if _, e := m.Policy().DecideCommand("mcpshka-storage-"+action, nil); e != nil {
				return nil, storagefabric.Result{}, e
			}
			out, e := storageCall(ctx, m, action, in)
			return nil, out, e
		})
	}
}
