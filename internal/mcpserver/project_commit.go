package mcpserver

import (
	"mcpshka/internal/gitops"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/tools/projectops"
)

type ProjectCommitInput = projectops.ProjectCommitInput
type ProjectWorktreeInput = projectops.ProjectWorktreeInput

func registerProjectCommit(r *toolRegistry, git *gitops.Service, leases *projectlease.Service) {
	projectops.RegisterCommit(r.install, r.store, git, leases)
}
func registerProjectWorktree(r *toolRegistry, git *gitops.Service, leases *projectlease.Service) {
	projectops.RegisterWorktree(r.install, r.store, git, leases)
}
