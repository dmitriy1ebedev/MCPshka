package fstools

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/tooldef"
	"path/filepath"
)

type MoveInput struct {
	Source         string `json:"source"`
	Destination    string `json:"destination"`
	Overwrite      bool   `json:"overwrite,omitempty"`
	LeaseToken     string `json:"lease_token,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}
type MkdirInput struct {
	Path           string `json:"path"`
	LeaseToken     string `json:"lease_token,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (d Deps) withWrite(paths []string, token string, apply func() error) error {
	anchors := make([]string, 0, len(paths))
	for _, p := range paths {
		a, _ := filesystem.ExistingAnchor(filepath.Clean(p))
		real, err := d.Manager.Policy().CheckWorkdir(a)
		if err != nil {
			return err
		}
		anchors = append(anchors, real)
	}
	return d.Manager.WithIdleProjects(anchors, func() error {
		for _, a := range anchors {
			if token == "" {
				if err := d.Guard(a, ""); err != nil {
					return err
				}
			} else {
				if err := d.Pipelines.Guard(a, ""); err != nil {
					return err
				}
			}
		}
		if token != "" {
			return d.Leases.WithInteractiveHolders(anchors, token, apply)
		}
		return apply()
	})
}
func registerOperations(add func(tooldef.Def), d Deps) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "fs_move", Description: "Rename or move an allowed file or directory atomically. Refuses overwrite by default, symlink endpoints and allowed roots; no cross-device copy. Both paths require idle-project and lease admission.", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in MoveInput) (*mcp.CallToolResult, filesystem.Entry, error) {
		var out filesystem.Entry
		err := d.withWrite([]string{in.Source, in.Destination}, in.LeaseToken, func() error {
			var err error
			out, err = d.Files.Move(in.Source, in.Destination, in.Overwrite)
			return err
		})
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "fs_mkdir", Description: "Create an allowed directory and up to eight missing parents, like mkdir -p, under the same write and lease policies as fs_write.", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in MkdirInput) (*mcp.CallToolResult, filesystem.Entry, error) {
		var out filesystem.Entry
		err := d.withWrite([]string{filepath.Join(in.Path, ".probe")}, in.LeaseToken, func() error { var err error; out, err = d.Files.Mkdir(in.Path); return err })
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "fs_read_many", Description: "Read 1..16 allowed files with an aggregate byte budget, at most 64 KiB. Alias of fs_read files; each failed path carries its own error.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in FSReadInput) (*mcp.CallToolResult, []filesystem.BatchFile, error) {
		out, err := d.Files.ReadMany(in.Files, in.Limit)
		return nil, out, err
	}))
}
