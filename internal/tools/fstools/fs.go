package fstools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/trust"
	"mcpshka/internal/workflow"
	"path/filepath"
	"time"
)

func Register(add func(tooldef.Def), d Deps) {
	registerPDF(add, d)
	registerOperations(add, d)
	registerSearch(add, d)
	manager := d.Manager
	files := d.Files
	artifactManager := d.Artifacts
	leases := d.Leases
	pipelines := d.Pipelines
	guard := d.Guard
	idempotencyStore := d.Store
	closed := false
	destructive := true
	nonDestructive := false

	add(tooldef.New(&mcp.Tool{
		Name:        "fs_list",
		Description: "List an allowed directory with bounded recursive depth and entry count. Symlinks are reported but never followed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in FSListInput) (*mcp.CallToolResult, filesystem.ListResult, error) {
		out, err := files.List(in.Path, in.Depth, in.MaxEntries)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "fs_read",
		Description: "Read a bounded byte range, a tail, or from a 1-based line of an allowed regular file. UTF-8 is returned as text (a window never splits a character); other bytes are base64. Small files include SHA-256 for safe edits. files=[{path, sha256?}] reads up to 16 files in one call instead: an unchanged file (same sha256) comes back without text, a failing file carries its own error.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in FSReadInput) (*mcp.CallToolResult, filesystem.ReadResult, error) {
		if len(in.Files) > 0 {
			batch, err := files.ReadMany(in.Files, in.Limit)
			return nil, filesystem.ReadResult{Trust: trust.Untrusted, Files: batch}, err
		}
		cursor := in.Cursor
		if in.Line > 0 {
			off, err := files.LineOffset(in.Path, in.Line)
			if err != nil {
				return nil, filesystem.ReadResult{}, err
			}
			cursor = off
		}
		out, err := files.Read(in.Path, cursor, in.Limit, in.Tail)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "fs_find",
		Description: "Search file names or text (literal, or RE2 with regex=true) under an allowed path, minus ignore globs. Bounded by results and time: status max_results or timed_out carries a cursor to continue; partial means some paths were unreadable (skipped.denied, denied_examples) while the other matches stand. Files over 4 MiB and binary files are counted in skipped; .git, node_modules and vendor are not walked.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in FSFindInput) (*mcp.CallToolResult, filesystem.FindResult, error) {
		out, err := files.Search(filesystem.FindQuery{Path: in.Path, Pattern: in.Pattern, Mode: in.Mode, Regex: in.Regex, Ignore: in.Ignore,
			Cursor: in.Cursor, MaxResults: in.MaxResults, Timeout: time.Duration(in.TimeoutMS) * time.Millisecond, ContextLines: in.ContextLines})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "fs_patch",
		Description: "Atomically replace exact UTF-8 text in an allowed file. Requires the SHA-256 observed by fs_read and an exact replacement count to reject stale or ambiguous writes.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in FSPatchInput) (*mcp.CallToolResult, filesystem.PatchResult, error) {
		// Bind retry identity to the holder secret without recording the secret
		// itself in durable idempotency metadata, logs or audit.
		holderDigest := sha256.Sum256([]byte(in.LeaseToken))
		payload := struct {
			Path                 string
			OldText              string
			NewText              string
			ExpectedSHA256       string
			ExpectedReplacements int
			LeaseTokenSHA256     string
		}{in.Path, in.OldText, in.NewText, in.ExpectedSHA256, in.ExpectedReplacements, hex.EncodeToString(holderDigest[:])}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "fs_patch", payload, func() (filesystem.PatchResult, error) {
			var out filesystem.PatchResult
			err := manager.WithIdleProject(filepath.Dir(in.Path), func() error {
				apply := func() error {
					var e error
					out, e = files.Patch(in.Path, in.OldText, in.NewText, in.ExpectedSHA256, in.ExpectedReplacements)
					return e
				}
				if in.LeaseToken == "" {
					if e := guard(filepath.Dir(in.Path), ""); e != nil {
						return e
					}
					return apply()
				}
				// A holder token does not override workflow/deployment fencing.
				if e := pipelines.Guard(filepath.Dir(in.Path), ""); e != nil {
					return e
				}
				if e := leases.WithInteractiveHolder(filepath.Dir(in.Path), in.LeaseToken, apply); e != nil {
					return e
				}
				leases.NoteWrite(in.Path, in.LeaseToken, out.NewSHA256)
				return nil
			})
			return out, err
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "fs_stat",
		Description: "Inspect file, directory or symlink metadata without following the final symlink; a missing entry is reported explicitly.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in FSStatInput) (*mcp.CallToolResult, filesystem.Entry, error) {
		out, err := files.StatPath(in.Path)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "fs_write",
		Description: "Create or SHA-pinned atomic replacement of a regular file within allowed roots; missing parent folders (up to 8) are created. Accepts base64 data up to 64 KiB; append=true adds it to the end (files up to 4 MiB in chunks). Refuses symlink destination or stale file. No delete or chmod.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in FSWriteInput) (*mcp.CallToolResult, filesystem.AtomicWriteResult, error) {
		payload := struct {
			Path, ContentBase64, ExpectedSHA256, LeaseTokenSHA256 string
			Append                                                bool
		}{in.Path, in.ContentBase64, in.ExpectedSHA256, HolderTokenDigest(in.LeaseToken), in.Append}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "fs_write", payload, func() (filesystem.AtomicWriteResult, error) {
			var result filesystem.AtomicWriteResult
			// A new file may need new folders (.gitverse/workflows): every check
			// runs on the deepest existing folder, and only then are the missing
			// ones created under it (08.11: ChatGPT and Claude got «lstat: no such file»).
			anchor, missing := filesystem.ExistingAnchor(filepath.Clean(in.Path))
			err := manager.WithIdleProject(anchor, func() error {
				apply := func() error {
					if len(missing) > 0 {
						if err := files.CanCreateIn(anchor); err != nil {
							return err
						}
					}
					if err := filesystem.MakeMissing(missing); err != nil {
						return err
					}
					var writeErr error
					if in.Append {
						result, writeErr = files.AtomicAppend(in.Path, in.ContentBase64, in.ExpectedSHA256)
					} else {
						result, writeErr = files.AtomicWrite(in.Path, in.ContentBase64, in.ExpectedSHA256)
					}
					if writeErr != nil {
						filesystem.RemoveMissing(missing)
					}
					return writeErr
				}
				if in.LeaseToken == "" {
					if err := guard(anchor, ""); err != nil {
						return err
					}
					return apply()
				}
				// Same holder path as fs_patch: workflow fencing still applies.
				if err := pipelines.Guard(anchor, ""); err != nil {
					return err
				}
				if err := leases.WithInteractiveHolder(anchor, in.LeaseToken, apply); err != nil {
					return err
				}
				leases.NoteWrite(in.Path, in.LeaseToken, result.NewSHA256)
				return nil
			})
			return result, err
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "artifact_get",
		Description: "Create a short-lived download ticket for an allowed regular file or, with repo, a verified pipeline package export of its HEAD. The file bytes are not returned through MCP context. Use download_path on the authenticated HTTP transport.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ArtifactGetInput) (*mcp.CallToolResult, artifacts.Ticket, error) {
		principal := ""
		if identity, ok := authn.IdentityFromContext(ctx); ok {
			principal = identity.ID
		}
		if in.Repo != "" {
			out, err := artifactManager.CreatePipelineFor(in.Repo, in.Path, principal)
			return nil, out, err
		}
		out, err := artifactManager.CreateFor(in.Path, principal)
		return nil, out, err
	}))
}

// holderTokenDigest keeps lease tokens out of idempotency receipts; an empty
// token stays empty so tokenless requests keep their original fingerprint.
func HolderTokenDigest(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type FSListInput = toolcontract.FSListInput
type FSReadInput = toolcontract.FSReadInput
type FSFindInput = toolcontract.FSFindInput
type FSPatchInput = toolcontract.FSPatchInput
type FSStatInput = toolcontract.FSStatInput
type FSWriteInput = toolcontract.FSWriteInput
type ArtifactGetInput = toolcontract.ArtifactGetInput
type Deps struct {
	Manager   *jobs.Manager
	Files     *filesystem.Service
	Artifacts *artifacts.Manager
	Leases    *projectlease.Service
	Pipelines *workflow.Engine
	Guard     func(string, string) error
	Store     *idempotency.Store
}
