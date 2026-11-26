package projectops

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/artifactcleanup"
	"mcpshka/internal/audit"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/tooldef"
)

type ArtifactCleanupInput struct {
	Path           string                 `json:"path" jsonschema:"Exact project worktree."`
	Mode           string                 `json:"mode" jsonschema:"preview or apply; apply repeats the exact metadata returned by preview."`
	Files          []artifactcleanup.File `json:"files" jsonschema:"Explicit relative artifact paths; apply requires bytes, sha256, inode and owner from preview."`
	LeaseToken     string                 `json:"lease_token"`
	IdempotencyKey string                 `json:"idempotency_key"`
}

func registerArtifactCleanup(add func(tooldef.Def), d Deps) {
	closed, destructive := false, true
	add(tooldef.New(&mcp.Tool{Name: "project_artifact_cleanup", Description: "Preview or remove only explicitly listed disposable worktree artifacts under the exact holder lease, with inode/owner/bytes/SHA-256 checks. Tracked files, symlinks, production and rollback paths are protected. A live project job blocks the entire operation.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in ArtifactCleanupInput) (*mcp.CallToolResult, artifactcleanup.Report, error) {
		var out artifactcleanup.Report
		if in.Mode != "preview" && in.Mode != "apply" {
			return nil, out, errors.New("mode must be preview or apply")
		}
		run := func() (artifactcleanup.Report, error) {
			err := d.Manager.WithIdleProject(in.Path, func() error {
				return d.Leases.WithInteractiveHolder(in.Path, in.LeaseToken, func() error {
					status, err := d.Git.Status(in.Path, 4096)
					if err != nil {
						return err
					}
					if status.Root != in.Path {
						return errors.New("exact worktree root required")
					}
					tracked, err := artifactTracked(ctx, in.Path)
					if err != nil {
						return err
					}
					out, err = artifactcleanup.Run(in.Path, in.Files, in.Mode == "apply", tracked)
					return err
				})
			})
			return out, err
		}
		var err error
		if in.Mode == "preview" {
			out, err = run()
		} else {
			if strings.TrimSpace(in.IdempotencyKey) == "" {
				return nil, out, errors.New("idempotency_key required")
			}
			payload := in
			payload.LeaseToken = d.Digest(in.LeaseToken)
			out, err = idempotency.Run(d.Store, in.IdempotencyKey, "project_artifact_cleanup", payload, run)
			if logger := d.Manager.AuditLogger(); logger != nil {
				_ = logger.Log(audit.Event{Type: "project_artifact_cleanup", Workdir: in.Path, Details: map[string]any{"removed_bytes": out.RemovedBytes}})
			}
		}
		return nil, out, err
	}))
}
