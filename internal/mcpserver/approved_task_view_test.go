package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 27.10: admin_coverage(approved_task_execute) said approval_grant and
// pointed at mutation_approval_request; the task has its own Face ID.
func TestCoverageNamesPlanFaceIDForApprovedTask(t *testing.T) {
	r := &toolRegistry{catalog: map[string]*mcp.Tool{"approved_task_execute": {Name: "approved_task_execute"}}, tools: map[string]bool{}}
	out, err := r.coverage(context.Background(), AdminCoverageInput{Tool: "approved_task_execute", SessionID: "x"})
	if err != nil || out.Needs != "plan_face_id" || out.Covered || strings.Contains(out.Action, "mutation_approval_request") || !strings.Contains(out.Action, "same idempotency_key") {
		t.Fatalf("%+v %v", out, err)
	}
}
