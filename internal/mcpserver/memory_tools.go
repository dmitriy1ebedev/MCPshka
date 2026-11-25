package mcpserver

import (
	"mcpshka/internal/audit"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/jobs"
	"mcpshka/internal/memory"
	"mcpshka/internal/tools/memorytools"
)

func registerMemory(r *toolRegistry, m *jobs.Manager) {
	var night func()
	if brokerclient.Enabled() {
		night = func() {
			memory.Night(m.StateDir(), func(line string) { _ = m.AuditLogger().Log(audit.Event{Type: "memory_consolidate", Result: line}) })
		}
	}
	memorytools.Register(r.install, m.StateDir(), night, requireActiveAdmin)
}
