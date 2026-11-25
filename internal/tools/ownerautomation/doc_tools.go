package ownerautomation

import (
	"context"
	"errors"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/adminops"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/forgeci"
	"mcpshka/internal/jobs"
	"mcpshka/internal/officedocs"
	"mcpshka/internal/trust"
)

type DocRun struct {
	ID    string        `json:"id"`
	Job   jobs.Snapshot `json:"job"`
	Next  string        `json:"next"`
	Trust trust.Label   `json:"trust"`
}

func docJobOptions() jobs.StartOptions {
	// LibreOffice takes ~300 MB; the scope keeps it off Home Assistant's memory.
	return jobs.StartOptions{Timeout: 240 * time.Second, OutputLimitBytes: 4 << 20, MemoryLimitBytes: 900 << 20,
		CPUTimeLimit: 200 * time.Second, CPUQuotaPercent: 150, ProcessLimit: 256, PrivateTemp: true}
}

// docSource resolves what the run reads: a chat upload, a download, an earlier result, or a policy-readable file.
func docSource(ctx context.Context, m *jobs.Manager, d Deps, in officedocs.Input, id string) (string, error) {
	if in.ArtifactID == "" {
		return officedocs.Source(in.Path, m.Policy().ResolveExisting)
	}
	up, err := d.Inbox.Status(d.Owner(ctx), in.ArtifactID)
	if err != nil || up.State != "ready" {
		return "", errors.New("a ready uploaded artifact is required")
	}
	return officedocs.Adopt(id, up.Name, up.Path)
}

func RegisterDoc(add func(tooldef.Def), m *jobs.Manager, d Deps) {
	closed, destructive := false, false
	start := func(ctx context.Context, action string, in officedocs.Input) (DocRun, error) {
		if _, err := m.Policy().DecideCommand("mcpshka-doc-"+action, nil); err != nil {
			return DocRun{}, err
		}
		spec := officedocs.NewSpec(action, in)
		if spec.ActsAsOwner() {
			owner, err := adminops.Owner(ctx)
			if err != nil {
				return DocRun{}, err
			}
			session, err := adminops.Run[struct {
				State string `json:"state"`
			}](ctx, "status", in.SessionID, owner)
			if err != nil || session.State != "active" {
				return DocRun{}, errors.New("script and macro_run execute code on H96: an active Face ID window (admin_session_id) is required; ops alone need none")
			}
		}
		return idempotency.Run(d.Store, d.Owner(ctx)+"\x00"+in.IdempotencyKey, "doc_"+action, in, func() (DocRun, error) {
			id, err := officedocs.NewID()
			if err != nil {
				return DocRun{}, err
			}
			spec.ID = id
			source, err := docSource(ctx, m, d, in, id)
			if err == nil {
				err = spec.Prepare(source, in.SaveAs)
			}
			if err == nil {
				err = spec.Write(time.Now())
			}
			if err != nil {
				return DocRun{}, err
			}
			snap, err := m.StartWithOptions(forgeci.Command, []string{"doc-run", officedocs.SpecPath(id)}, forgeci.Root, docJobOptions())
			if err != nil {
				_ = os.Remove(officedocs.SpecPath(id))
				return DocRun{}, err
			}
			return DocRun{ID: id, Job: snap, Next: officedocs.Next(action, snap.ID), Trust: trust.Untrusted}, nil
		})
	}
	add(tooldef.New(&mcp.Tool{
		Name:        "doc_inspect",
		Description: officedocs.InspectHelp,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in officedocs.Input) (*mcp.CallToolResult, DocRun, error) {
		out, err := start(ctx, "inspect", in)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "doc_edit",
		Description: officedocs.EditHelp,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in officedocs.Input) (*mcp.CallToolResult, DocRun, error) {
		out, err := start(ctx, "edit", in)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "doc_preview",
		Description: officedocs.PreviewHelp,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in officedocs.PreviewInput) (*mcp.CallToolResult, map[string]any, error) {
		if !officedocs.ValidID(in.ID) {
			return nil, nil, errors.New("invalid run id")
		}
		png, err := os.ReadFile(officedocs.PreviewPath(in.ID))
		if err != nil {
			return nil, nil, errors.New("no preview for this run: start it with preview=true and wait for the job")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: png}}}, map[string]any{"trust": trust.Untrusted, "id": in.ID, "bytes": len(png)}, nil
	}))
}
