// Package opencoretools publishes the open core (0.19.1, owner 25.11): the
// working MCPshka without the owner's journals and identifiers, on a public
// GitHub repository where its CI runs free. Secrets never were in Git; the
// export only removes private history and replaces addresses (internal/opencore).
package opencoretools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/opencore"
	"mcpshka/internal/tooldef"
)

// Root is the canonical checkout the release conveyor publishes from.
var Root = "/root/repos/mcpshka"

const work = "/var/lib/mcpshka/opencore"

type PreviewInput struct {
	Commit string `json:"commit,omitempty" jsonschema:"Full 40-hex commit of the canonical repository; default its main."`
}

type PublishInput struct {
	Commit         string `json:"commit,omitempty" jsonschema:"Full 40-hex commit of the canonical main to publish; default main."`
	Version        string `json:"version,omitempty" jsonschema:"Release shown in the public commit title, e.g. 0.19.1."`
	AdminSessionID string `json:"admin_session_id" jsonschema:"Active owner Face ID session."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type Result struct {
	Manifest opencore.Manifest `json:"manifest"`
	Publish  json.RawMessage   `json:"publish,omitempty"`
	Next     string            `json:"next,omitempty"`
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func resolve(ctx context.Context, commit string) (string, error) {
	if commit == "" {
		out, err := exec.CommandContext(ctx, "git", "-C", Root, "rev-parse", "refs/heads/main").Output()
		if err != nil {
			return "", errors.New("canonical main not readable")
		}
		commit = strings.TrimSpace(string(out))
	}
	if !shaRE.MatchString(commit) {
		return "", errors.New("commit must be a full 40-hex SHA")
	}
	if exec.CommandContext(ctx, "git", "-C", Root, "merge-base", "--is-ancestor", commit, "refs/heads/main").Run() != nil {
		return "", errors.New("only commits already in the canonical main are published")
	}
	return commit, nil
}

// export rebuilds the tree of commit under the service's work folder.
func export(ctx context.Context, commit string) (string, opencore.Manifest, error) {
	if err := os.MkdirAll(work, 0o700); err != nil {
		return "", opencore.Manifest{}, err
	}
	dest := filepath.Join(work, "tree-"+commit[:12])
	os.RemoveAll(dest)
	m, err := opencore.Export(ctx, Root, commit, dest, opencore.HostRules(ctx))
	if len(m.Audit.Findings) > 40 {
		m.Audit.Findings, m.Audit.Truncated = m.Audit.Findings[:40], true
	}
	return dest, m, err
}

// Register installs the read-only preview and the Face ID-gated publish.
func Register(add func(tooldef.Def), store *idempotency.Store, requireAdmin func(context.Context, string) error) {
	destructive, closed := false, false
	add(tooldef.New(&mcp.Tool{
		Name:        "opencore_preview",
		Description: "Open core dry run: export the canonical commit (default main) without private journals and with the owner's addresses replaced, audit it and report files, excluded paths, tree digest and any blocking finding. Publishes nothing.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PreviewInput) (*mcp.CallToolResult, Result, error) {
		commit, err := resolve(ctx, in.Commit)
		if err != nil {
			return nil, Result{}, err
		}
		dest, m, err := export(ctx, commit)
		os.RemoveAll(dest)
		res := Result{Manifest: m, Next: "opencore_publish with the same commit and an active Face ID session"}
		if err != nil {
			res.Next = "fix the blocking findings in the canonical tree (or the export policy " + opencore.ConfigPath + "), commit, preview again"
		}
		return nil, res, nil
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "opencore_publish",
		Description: "Publish the open core of a canonical main commit to every public repository in .mcpshka/opencore.json (GitHub, GitVerse, MosHub, SourceCraft): same export as opencore_preview (blocked on any secret or owner identifier), committed on top of the public main without force. Its CI runs there; the tree digest ties it to the source commit.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PublishInput) (*mcp.CallToolResult, Result, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, Result{}, errors.New("idempotency_key is required")
		}
		if err := requireAdmin(ctx, in.AdminSessionID); err != nil {
			return nil, Result{}, err
		}
		out, err := idempotency.Run(store, in.IdempotencyKey, "opencore_publish", in, func() (Result, error) {
			commit, err := resolve(ctx, in.Commit)
			if err != nil {
				return Result{}, err
			}
			dest, m, err := export(ctx, commit)
			if err != nil {
				return Result{Manifest: m}, err
			}
			defer os.RemoveAll(dest)
			// Detached from the request: a client that gives up after 60 s must
			// not cut the slowest site off mid-push (25.11: SourceCraft lagged).
			child, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			// Every target gets the same tree; one failing site does not stop the others.
			var all []json.RawMessage
			var failed []string
			for _, t := range m.Targets {
				raw, err := opencore.Publish{Tree: dest, Commit: commit, Digest: m.Digest, Version: in.Version, Target: t}.Run(child)
				all = append(all, raw)
				if err != nil {
					failed = append(failed, t.Site)
				}
			}
			raw, _ := json.Marshal(all)
			res := Result{Manifest: m, Publish: raw}
			if len(failed) > 0 {
				return res, errors.New("open core push did not complete on: " + strings.Join(failed, ", ") + "; repeat with the same idempotency_key after fixing")
			}
			return res, nil
		})
		return nil, out, err
	}))
}
