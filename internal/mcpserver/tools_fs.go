package mcpserver

import "mcpshka/internal/tools/fstools"

var holderTokenDigest = fstools.HolderTokenDigest

func registerFilesystemTools(registry *toolRegistry, d *serverDeps) {
	fstools.Register(registry.install, fstools.Deps{Manager: d.manager, Files: d.files, Artifacts: d.artifactManager, Leases: d.leases, Pipelines: d.pipelines, Guard: d.guard, Store: d.idempotencyStore})
}
