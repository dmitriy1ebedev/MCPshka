package mcpserver

import (
	"context"
	"errors"
	"mcpshka/internal/tools/fixes"
	"mcpshka/internal/tools/projectops"
	"strings"
)

func registerProjectTools(registry *toolRegistry, d *serverDeps) {
	issueService.FixesNight = fixes.Register(registry.install, d.manager, d.git, d.leases, d.idempotencyStore, func(ctx context.Context, name string, args map[string]any) (any, error) {
		res, err := registry.invoke(ctx, ToolInvokeInput{Name: name, Arguments: args})
		if err == nil && res.IsError {
			err = errors.New(strings.Join(res.Content, " "))
		}
		return res.Output, err
	})
	registerProjectCreate(registry, d.profiles)
	registerProjectCommit(registry, d.git, d.leases)
	registerProjectWorktree(registry, d.git, d.leases)
	registerPipelineTool(registry, d)
	projectops.Register(registry.install, projectops.Deps{Manager: d.manager, Git: d.git, Projects: d.projects, Profiles: d.profiles, Deploys: d.deploys, Verifier: d.verifier, Leases: d.leases, Store: d.idempotencyStore, Digest: holderTokenDigest, RecordProductionSource: d.recordProductionSource, RecordGate: recordGate})
}
