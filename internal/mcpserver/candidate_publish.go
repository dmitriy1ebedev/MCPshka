package mcpserver

import (
	"context"
	"errors"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"strings"
)

// exec_start's contract ledger binds the retry key to the complete input and
// records the receipt; root's sealed approval independently makes replay safe.
func candidatePublishCompat(ctx context.Context, m *jobs.Manager, leases *projectlease.Service, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Args) != 3 || in.Args[1] != "publish" || !executorCanaryID.MatchString(in.Args[2]) || strings.TrimSpace(in.IdempotencyKey) == "" || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("candidate publication requires sealed approval and retry identity; no overrides")
	}
	root, err := m.Policy().CheckWorkdir(in.Workdir)
	if err != nil || root != "/root/repos/mcpshka" {
		return jobs.Snapshot{}, errors.New("candidate publication scope invalid: HOLD")
	}
	if _, err := m.Policy().DecideCommandForWorkdir(in.Command, in.Args, root); err != nil {
		return jobs.Snapshot{}, err
	}
	var raw []byte
	err = candidateCleanupAdmission(m, leases, root, func() error {
		var e error
		raw, e = brokerclient.Run(ctx, "executor", "candidate-publish", in.Args[2])
		return e
	})
	if err != nil {
		return jobs.Snapshot{}, err
	}
	return m.StoreReceipt("mcpshka", in.Args, root, string(raw))
}
