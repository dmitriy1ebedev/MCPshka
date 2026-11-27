package mcpserver

import "mcpshka/internal/tools/hostops"

func registerHostTools(registry *toolRegistry, d *serverDeps) {
	hostops.RegisterHost(registry.install, hostops.Deps{Manager: d.manager, Store: d.idempotencyStore, Docker: d.dockerService, Control: d.controlService, Services: d.systemdServices, HostInventory: d.makeHostInventory})
}
