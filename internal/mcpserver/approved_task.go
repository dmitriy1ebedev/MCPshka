package mcpserver

import "mcpshka/internal/tools/approvedtask"

// approved_task_* live in internal/tools/approvedtask (0.19.0).
func registerApprovedTask(r *toolRegistry) {
	approvedtask.RegisterReview(r.server)
	approvedtask.RegisterResult(r.install, r.call)
	approvedtask.Register(r.install, adminOwner)
}
