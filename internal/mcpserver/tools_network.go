package mcpserver

import (
	"mcpshka/internal/tools/lanetools"
	"mcpshka/internal/tools/networktools"
)

type EndpointProbeInput = networktools.EndpointProbeInput

func registerNetworkTools(registry *toolRegistry, d *serverDeps) {
	lanetools.Register(registry.install, d.idempotencyStore)
	networktools.Register(registry.install, networktools.Deps{Manager: d.manager, Store: d.idempotencyStore, Network: d.networkService, Control: d.controlService, Lane: d.laneService})
}
