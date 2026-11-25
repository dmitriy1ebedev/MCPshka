package mcpserver

import (
	"mcpshka/internal/agents"
	"mcpshka/internal/jobs"
	"mcpshka/internal/systeminfo"
	"mcpshka/internal/tools/orchestration"
	"time"
)

var hostBootTime = systeminfo.BootTime
var agentTaskRead = agents.ReadTask

type AgentTaskInput = orchestration.AgentTaskInput
type AgentTaskCancelInput = orchestration.AgentTaskCancelInput
type AgentTaskListInput = orchestration.AgentTaskListInput
type AgentTaskStatusInput = orchestration.AgentTaskStatusInput

func registerAgentTools(r *toolRegistry, managers ...*jobs.Manager) {
	orchestration.RegisterAgents(r.install, orchestration.AgentDeps{Store: r.store, BootTime: func() time.Time { return hostBootTime() }, TaskRead: func(id string) (agents.Task, error) { return agentTaskRead(id) }, LeaseHeldBy: r.leaseHeldBy, ReleaseTaskLease: r.releaseTaskLease}, managers...)
}
