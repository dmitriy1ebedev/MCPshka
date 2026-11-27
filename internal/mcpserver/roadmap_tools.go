package mcpserver

import (
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/orchestration"
	"time"
)

type RoadmapRunInput = orchestration.RoadmapRunInput
type RoadmapRunStatusInput = orchestration.RoadmapRunStatusInput
type RoadmapRunStatusResult = orchestration.RoadmapRunStatusResult

func registerRoadmapTools(r *toolRegistry, manager *jobs.Manager) {
	git := gitops.New(manager.Policy())
	orchestration.RegisterRoadmap(r.install, manager, orchestration.RoadmapDeps{Store: r.store, Leases: r.leases, BootTime: func() time.Time { return hostBootTime() }, Gate: func(root, id string) string {
		gate, err := evaluatePhaseGate(git, manager, PhaseGateInput{Path: root, Phase: id})
		if err != nil {
			return err.Error()
		}
		return map[bool]string{false: gate.NextAction}[gate.Verdict == "PASS"]
	}})
}
