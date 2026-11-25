package mcpserver

import (
	"context"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/issues"
	"mcpshka/internal/jobs"
	"mcpshka/internal/memory"
	issuetools "mcpshka/internal/tools/issues"
)

func newIssueService() *issuetools.Service {
	return issuetools.New(issuetools.Deps{Version: Version, Root: releaseRoot, Owner: adminOwner, MemoryExam: memory.Gate, RPC: func(ctx context.Context, args ...string) ([]byte, error) {
		return brokerclient.Run(ctx, "admin-session", args...)
	}})
}

var issueService = newIssueService()

func issueStore() *issues.Store { return issueService.Store() }
func verifyIssueEvidence(i issues.Issue, evidence string, fromOwnerPage bool) error {
	return issueService.VerifyEvidence(i, evidence, fromOwnerPage)
}
func registerIssueTools(r *toolRegistry, manager *jobs.Manager) {
	issueService.Register(r.install, manager, r.store)
}

type IssueGetResult = issuetools.IssueGetResult
type IssueListInput = issuetools.IssueListInput
type IssueListResult = issuetools.IssueListResult
type IssueIDInput = issuetools.IssueIDInput
type IssueCreateInput = issuetools.IssueCreateInput
type IssueCreateResult = issuetools.IssueCreateResult
type IssueUpdateInput = issuetools.IssueUpdateInput
type IssueAcceptInput = issuetools.IssueAcceptInput
type IssueImportInput = issuetools.IssueImportInput
type IssueDispatchInput = issuetools.IssueDispatchInput
