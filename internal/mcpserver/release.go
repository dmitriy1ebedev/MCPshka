package mcpserver

import (
	"context"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/releaseops"
)

const releaseRoot = releaseops.Root
const releaseRunner = releaseops.Runner

type ReleasePrepareInput = releaseops.ReleasePrepareInput
type ReleaseStatusInput = releaseops.ReleaseStatusInput
type ReleaseStatus = releaseops.ReleaseStatus
type ReleaseLocalInput = releaseops.ReleaseLocalInput

var releaseJobInput = releaseops.JobInput
var readRelease = releaseops.Read

func registerReleases(r *toolRegistry, m *jobs.Manager, store *idempotency.Store) {
	releaseops.Register(r.install, m, store, uploadOwner)
}
func releasePrepareCompat(ctx context.Context, m *jobs.Manager, store *idempotency.Store, in ExecStartInput) (jobs.Snapshot, error) {
	return releaseops.Compat(ctx, m, store, in, uploadOwner)
}
