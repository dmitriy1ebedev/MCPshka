package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/hostdiag"
)

type HostDiagnoseInput struct {
	Symptom string `json:"symptom,omitempty" jsonschema:"general (default), ssh_drop, thread_eagain, hang, oom or slow."`
}

func registerHostDiagnose(r *toolRegistry) {
	addTool(r, &mcp.Tool{Name: "host_diagnose", Description: "Explain a host symptom (ssh_drop, thread_eagain, hang, oom, slow, general) as FACT → INFERENCE: load per core, memory, 2 s vmstat reclaim/swap sample, OOM counter, threads, per-service cgroup tasks and RLIMIT_NPROC, kernel and sshd journal facts (24 h, broker). Verdict CAUSE_FOUND, NO_FAULT_FOUND or UNKNOWN with the checks that would decide it. Read-only.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in HostDiagnoseInput) (*mcp.CallToolResult, hostdiag.Result, error) {
		symptom := strings.TrimSpace(in.Symptom)
		if symptom == "" {
			symptom = "general"
		}
		if !hostdiag.Symptoms[symptom] {
			return nil, hostdiag.Result{}, errors.New("symptom must be general, ssh_drop, thread_eagain, hang, oom or slow")
		}
		var events *hostdiag.Events
		if raw, err := brokerclient.Run(ctx, "health", "host_events"); err == nil && len(raw) <= 64<<10 {
			var v hostdiag.Events
			if json.Unmarshal(raw, &v) == nil {
				events = &v
			}
		}
		return nil, hostdiag.Diagnose(ctx, symptom, events, 2*time.Second), nil
	})
}
