package mcpserver

import (
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/forge"
)

type ForgeCIInput = forge.ForgeCIInput

var forgeJobInput = forge.JobInput
var forgeCIWorkdir = forge.Workdir

func registerForgeCI(r *toolRegistry, m interface {
	StartWithOptions(string, []string, string, jobs.StartOptions) (jobs.Snapshot, error)
}, store *idempotency.Store) {
	forge.Register(r.install, m, store, uploadOwner, holderTokenDigest)
}
