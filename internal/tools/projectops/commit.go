package projectops

import (
	"context"
	"fmt"
	"strings"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/tooldef"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/gitops"
	"mcpshka/internal/projectlease"
)

type ProjectCommitInput struct {
	Path           string            `json:"path" jsonschema:"repository or worktree under /root/repos"`
	LeaseToken     string            `json:"lease_token" jsonschema:"current project_lease_acquire token of this repository"`
	Files          []string          `json:"files" jsonschema:"1..200 repo-relative paths to commit (a file already gone from the working tree is committed as its deletion); nothing else is committed"`
	Delete         []string          `json:"delete,omitempty" jsonschema:"repo-relative files to remove from the working tree and commit as deleted (no rm or raw git needed); counted with files"`
	ExpectedFiles  map[string]string `json:"expected_files,omitempty" jsonschema:"optional complete map from files to SHA-256 of the reviewed raw bytes or DELETED for an already-absent file; checks the prospective commit index"`
	Message        string            `json:"message" jsonschema:"commit message"`
	IdempotencyKey string            `json:"idempotency_key"`
}

func RegisterCommit(add func(tooldef.Def), store *idempotency.Store, git *gitops.Service, leases *projectlease.Service) {
	add(tooldef.New(&mcp.Tool{Name: "project_commit", Description: "Commit exactly the listed files (and delete: files to remove) with a message as the service user, under your project lease (repository hooks disabled, other staged paths preserved). Supply expected_files to check the exact prospective Git index; use DELETED in files for removals that are already absent. Then project_test on the new HEAD. Returns the commit SHA and stat.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectCommitInput) (*mcp.CallToolResult, gitops.CommitResult, error) {
		out, err := idempotency.Run(store, in.IdempotencyKey, "project_commit", in, func() (gitops.CommitResult, error) {
			// Keep the interactive writer fence for the entire commit, not just
			// the preflight. Otherwise a concurrent fs_patch can run after
			// ValidateHolder and before the Git index/worktree snapshot (MCP-217).
			var committed gitops.CommitResult
			err := leases.WithInteractiveHolder(in.Path, in.LeaseToken, func() error {
				root, e := git.RepoRoot(in.Path)
				if e != nil {
					return e
				}
				if e := verifyCommitSources(root, in.Files, in.Delete, in.ExpectedFiles); e != nil {
					return e
				}
				// expected_files is the caller's own reviewed pin and wins.
				if in.ExpectedFiles == nil {
					if changed := leases.ForeignChanges(root, in.LeaseToken, in.Files); len(changed) > 0 {
						return fmt.Errorf("HOLD (MCP-217): %s changed after your last fs_write/fs_patch under this lease — another writer? fs_read them and re-apply, or pass expected_files for the content you reviewed", strings.Join(changed, ", "))
					}
				}
				if in.ExpectedFiles != nil {
					committed, e = git.CommitVerified(in.Path, in.Files, in.Message, in.ExpectedFiles)
				} else {
					committed, e = git.CommitChanges(in.Path, in.Files, in.Delete, in.Message)
				}
				return e
			})
			return committed, err
		})
		return nil, out, err
	}))
}

type ProjectWorktreeInput struct {
	gitops.WorktreeInput
	LeaseToken     string `json:"lease_token,omitempty" jsonschema:"remove: your lease token on the worktree path"`
	IdempotencyKey string `json:"idempotency_key"`
}

func RegisterWorktree(add func(tooldef.Def), store *idempotency.Store, git *gitops.Service, leases *projectlease.Service) {
	add(tooldef.New(&mcp.Tool{Name: "project_worktree", Description: "Work branch checkouts without bash: op=create makes branch+worktree from base (default main) in /root/repos/wt as the service user, op=remove drops a clean worktree under your lease (the branch stays), op=list shows them. Then project_lease_acquire on the worktree path, fs_write, project_commit.", Annotations: &mcp.ToolAnnotations{}}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectWorktreeInput) (*mcp.CallToolResult, gitops.WorktreeResult, error) {
		out, err := idempotency.Run(store, in.IdempotencyKey, "project_worktree", in, func() (gitops.WorktreeResult, error) {
			if in.Op == "remove" {
				if _, err := leases.ValidateHolder(git.WorktreePath(in.Path, in.Branch), in.LeaseToken); err != nil {
					return gitops.WorktreeResult{}, err
				}
			}
			return git.Worktree(in.WorktreeInput)
		})
		return nil, out, err
	}))
}
