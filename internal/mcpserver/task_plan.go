package mcpserver

import (
	"context"
	"mcpshka/internal/tools/taskplan"
)

type TaskPlanInput = taskplan.TaskPlanInput
type TaskPlanStep = taskplan.TaskPlanStep
type TaskPlanResult = taskplan.TaskPlanResult

func (r *toolRegistry) taskPlanDeps() taskplan.Deps {
	return taskplan.Deps{Resolve: r.resolveCapabilities, Registered: func(n string) bool { return r.lookupTool(n).tool != nil }, Readiness: func(n string) string { return r.cachedReadiness(n).State }, Coverage: func(ctx context.Context, n string) (string, error) {
		c, e := r.coverage(ctx, AdminCoverageInput{Tool: n})
		return c.Needs, e
	}, Contract: r.contractFor}
}
func (r *toolRegistry) taskPlan(ctx context.Context, in TaskPlanInput) (TaskPlanResult, error) {
	return taskplan.Plan(ctx, in, r.taskPlanDeps())
}
func registerTaskPlan(r *toolRegistry) { taskplan.Register(r.install, r.taskPlanDeps()) }
