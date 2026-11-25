package hosttriage

import (
	"strings"
	"testing"
)

// MCP-046: a healthy host with a degraded executor is not "nothing to fix".
func TestAttachExecution(t *testing.T) {
	out := HostTriageResult{Healthy: true, NextAction: "nothing to fix"}
	AttachExecution(&out, "degraded", []string{"jobs_workflow=degraded"})
	if out.Execution == nil || out.Execution.State != "degraded" || !strings.Contains(out.NextAction, "jobs_workflow=degraded") || !strings.Contains(out.NextAction, "health_snapshot") {
		t.Fatalf("%+v", out)
	}
	ok := HostTriageResult{Healthy: true, NextAction: "nothing to fix"}
	AttachExecution(&ok, "ok", nil)
	if ok.NextAction != "nothing to fix" || ok.Execution.State != "ok" {
		t.Fatalf("%+v", ok)
	}
	// A sick host keeps its own repair action first.
	sick := HostTriageResult{Healthy: false, NextAction: "docker_restart"}
	AttachExecution(&sick, "", nil)
	if sick.NextAction != "docker_restart" || sick.Execution.State != "unknown" {
		t.Fatalf("%+v", sick)
	}
}
