package mcpserver

import (
	"mcpshka/internal/runnerhub"
	"mcpshka/internal/tools/orchestration"
)

type RunnerTaskInput = orchestration.RunnerTaskInput
type RunnerStatusInput = orchestration.RunnerStatusInput
type RunnerStatusResult = orchestration.RunnerStatusResult

func registerRunnerTools(r *toolRegistry, hub *runnerhub.Hub) {
	orchestration.RegisterRunners(r.install, hub)
}
