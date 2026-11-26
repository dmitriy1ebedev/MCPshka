package mcpserver

import (
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/tools/health"
)

var healthAction = health.Action
var healthStates = health.States
var criticalServicesHealth = health.CriticalServices

type healthBrokerRunner = health.BrokerRunner

func blockerHealthProbes(run healthBrokerRunner) []healthlayer.Probe {
	return health.BlockerProbes(run, Version)
}
