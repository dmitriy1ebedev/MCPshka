package mcpserver

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/diagprovider"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/hosttriage"
	"mcpshka/internal/jobs"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/systemdsvc"
	"time"
)

type HostTriageResult = hosttriage.HostTriageResult

func registerHostTriageTool(registry *toolRegistry, docker *dockerops.Service, services *systemdsvc.Service, manager *jobs.Manager, heal ...*selfheal.Service) {
	closed := false
	startIssueSentinel(docker, services, manager, heal...)
	addTool(registry, &mcp.Tool{
		Name:        "host_triage",
		Description: "«Разберись»: one read-only pass over the host — unhealthy/exited/restarting containers, inactive allowlisted services, failed systemd units, UNKNOWN MCPshka jobs, broken /etc/mcpshka config files (file and line only, never values), pressure per CPU — each with evidence and the exact MCPshka call to fix it (restart where allowlisted). When the host is not healthy it attaches the newest DIAG host investigation (diagnosis) or, if none ran in 15 min, a ready diag_investigation_start (problem kind=diagnosis; its fixed key reuses one run per 10 min). After the fix run DIAG again and diag_compare both runs. Run the actions, then call host_triage again to verify; report what stayed broken.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, HostTriageResult, error) {
		out := hosttriage.Run(ctx, docker, services, manager, heal...)
		attachExecution(ctx, registry.health, &out)
		hosttriage.AttachDiagnosis(ctx, &out, diagprovider.NewNative(manager))
		return nil, out, nil
	})
}

// attachExecution reads the cached health aggregate (15 s cache) under a
// short budget; a slow or missing collector is reported as unknown.
func attachExecution(ctx context.Context, health *healthlayer.Collector, out *HostTriageResult) {
	if health == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	rep, err := health.Read(cctx)
	if err != nil {
		hosttriage.AttachExecution(out, "unknown", []string{"health_snapshot: " + firstSentence(err.Error())})
		return
	}
	var notOK []string
	for _, item := range rep.Items {
		if item.State != "ok" {
			notOK = append(notOK, item.Name+"="+item.State)
		}
	}
	hosttriage.AttachExecution(out, rep.State, notOK)
}
