package mcpserver

import (
	"context"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/issues"
	"mcpshka/internal/issuesnap"
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
var verifyIssueEvidence = issueService.VerifyEvidence
func registerIssueTools(r *toolRegistry, manager *jobs.Manager) {
	issueService.SetSnapshot(issuesnap.With(Version, func(n string) issuesnap.Reader { return issuesnap.Reader(r.readers[n]) }))
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
