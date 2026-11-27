package mcpserver

import "mcpshka/internal/tools/hostops"

func registerSystemTools(registry *toolRegistry, d *serverDeps) {
	hostops.RegisterSystem(registry.install, hostops.Deps{Manager: d.manager, Store: d.idempotencyStore, Docker: d.dockerService, Control: d.controlService, Services: d.systemdServices, HostInventory: d.makeHostInventory})
}
