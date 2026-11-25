package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/tools/workyield"
)

type WorkYieldInput = workyield.WorkYieldInput
type WorkYieldStatusInput = workyield.WorkYieldStatusInput
type WorkYieldResult = workyield.WorkYieldResult

var yieldHash = workyield.Hash

func yieldProof(m *jobs.Manager, ctx context.Context, id string) (workyield.Journal, error) {
	return workyield.Proof(m, ctx, id, uploadOwner)
}
func workYield(ctx context.Context, m *jobs.Manager, s *projectlease.Service, in WorkYieldInput) (WorkYieldResult, error) {
	return workyield.Yield(ctx, m, s, in, uploadOwner)
}
func registerWorkYield(r *toolRegistry, m *jobs.Manager, s *projectlease.Service) {
	r.yieldRecovery = func(ctx context.Context, tool, key string, input map[string]any) (WorkYieldResult, error) {
		return workyield.Recover(ctx, m, tool, key, input, uploadOwner)
	}
	workyield.Register(r.install, m, s, uploadOwner)
}
func authorizeScopedWorkReceipt(ctx context.Context, m *jobs.Manager, id string) error {
	return workyield.AuthorizeReceipt(ctx, m, id, uploadOwner)
}

func workYieldCompat(ctx context.Context, m *jobs.Manager, s *projectlease.Service, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Args) != 2 || len(in.Args[1]) > 4096 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("work-yield requires bounded typed input without overrides")
	}
	if _, e := m.Policy().CheckWorkdir(in.Workdir); e != nil {
		return jobs.Snapshot{}, e
	}
	if _, e := m.Policy().DecideCommand("mcpshka-work-yield", nil); e != nil {
		return jobs.Snapshot{}, e
	}
	var p WorkYieldInput
	if e := json.Unmarshal([]byte(in.Args[1]), &p); e != nil {
		return jobs.Snapshot{}, e
	}
	if p.IdempotencyKey != in.IdempotencyKey {
		return jobs.Snapshot{}, errors.New("work-yield retry keys must match")
	}
	out, e := workYield(ctx, m, s, p)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	return m.StoreReceipt("mcpshka", []string{"work-yield", out.YieldID, "owner-sha256:" + yieldHash(uploadOwner(ctx))}, in.Workdir, string(raw))
}
