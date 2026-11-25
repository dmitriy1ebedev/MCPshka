package mcpserver

import (
	"mcpshka/internal/agents"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/jobs"
	"mcpshka/internal/mission"
	"mcpshka/internal/roadmaprun"
	missiontools "mcpshka/internal/tools/mission"
)

type missionReader = missiontools.Reader

func newMissionReader(m *jobs.Manager) *missionReader {
	return missiontools.New(missiontools.Deps{Manager: m, Tasks: agents.TaskDir, Runs: roadmaprun.StateDir, Projects: "/root/repos"})
}

func registerMissionTools(r *toolRegistry, s *missionReader, controllers ...*mission.Controller) {
	missiontools.Register(r.install, s, controllers...)
}

func missionHealthProbe(s *missionReader) healthlayer.Probe { return missiontools.HealthProbe(s) }
