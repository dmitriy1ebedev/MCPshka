package mcpserver

import (
	"mcpshka/internal/tools/cloud"
)

type CloudAgentTask = cloud.CloudAgentTask
type CloudAgentInput = cloud.CloudAgentInput
type CloudAgentOutput = cloud.CloudAgentOutput

var cloudAgentObserve = cloud.ObserveAgent

func registerCloudAgent(r *toolRegistry) { cloud.RegisterAgent(r.install) }
