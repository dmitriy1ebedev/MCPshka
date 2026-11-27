package mcpserver

import (
	"context"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledgeops"
	"mcpshka/internal/tools/knowledgetools"
)

type KnowledgeWriteInput = knowledgetools.KnowledgeWriteInput
type KnowledgeReadResult = knowledgetools.KnowledgeReadResult
type KnowledgeSearchResult = knowledgetools.KnowledgeSearchResult
type KnowledgeReadInput = knowledgetools.KnowledgeReadInput
type KnowledgeSearchInput = knowledgetools.KnowledgeSearchInput
type KnowledgeHealth = knowledgetools.KnowledgeHealth
type IndexHealth = knowledgetools.IndexHealth
type DIAGStartInput = knowledgetools.DIAGStartInput
type DIAGCompareInput = knowledgetools.DIAGCompareInput
type DIAGWifiScanInput = knowledgetools.DIAGWifiScanInput
type DIAGRunInput = knowledgetools.DIAGRunInput
type KnowledgeReindexInput = knowledgetools.KnowledgeReindexInput

func registerKnowledgeDIAG(r *toolRegistry, m *jobs.Manager) {
	knowledgetools.RegisterDIAG(r.install, m)
}

func knowledgeDIAGCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	return knowledgeops.Compat(ctx, m, knowledgeops.CompatRequest{
		Command: in.Command, Args: in.Args, Workdir: in.Workdir, Env: in.Env,
		ApprovalToken: in.ApprovalToken, PlanHash: in.PlanHash, IdempotencyKey: in.IdempotencyKey,
	})
}
