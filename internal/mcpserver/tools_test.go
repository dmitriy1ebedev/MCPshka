package mcpserver

import (
	"context"
	"encoding/json"
	"mcpshka/internal/approval"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

func TestToolSurfaceIncludesBoundedProjectTools(t *testing.T) {
	ctx := context.WithValue(context.Background(), fullCatalogKey{}, true) // hidden tools stay registered
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	server := New(manager, filesystem.New(p))
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, name := range []string{
		"protected_core_list", "device_status", "control_path_status", "mutation_preflight", "network_stage_status", "network_stage_apply", "network_stage_commit", "network_stage_rollback", "network_snapshot", "network_connectivity", "endpoint_probe", "diag_observer_status", "traffic_route_start", "traffic_route_status", "traffic_route_cancel", "traffic_route_report", "exec_classify", "approval_request", "approval_status", "approval_gate_status", "service_list", "service_dependencies", "service_status", "journal_read", "service_action", "docker_list", "docker_status", "docker_inspect", "docker_resources", "docker_logs", "docker_action", "exec_start", "idempotency_status", "job_status", "job_wait", "job_output", "job_cancel",
		"fs_list", "fs_read", "fs_find", "fs_patch",
		"git_status", "git_diff", "project_profile", "project_run", "project_test", "project_build", "project_verify",
		"project_deploy", "broker_install", "service_user_migrate", "project_deploy_status", "project_cleanup_candidates",
		"project_lease_acquire", "project_lease_status", "project_lease_heartbeat", "project_lease_release", "artifact_get", "artifact_upload_begin", "artifact_upload_chunk", "artifact_upload_status", "artifact_upload_finish", "artifact_upload_cancel", "codex_capacity_status", "project_work_yield", "project_work_yield_status", "interaction_recovery_diagnose", "health_snapshot", "mission_status", "self_heal_status",
	} {
		if !got[name] {
			t.Fatalf("missing MCP tool %q; got=%v", name, got)
		}
	}
}

func TestMCPExpiredApprovalHoldsWithoutStartingMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "jobs")
	pol := policy.Default()
	pol.AllowedWorkdirs = []string{root}
	pol.DefaultProfile = policy.ProfileOperator
	manager, err := jobs.New(state, pol)
	if err != nil {
		t.Fatal(err)
	}
	store := approval.New(filepath.Join(filepath.Dir(state), "approvals"))
	manager.SetApprovalStore(store)
	plan := strings.Repeat("a", 64)
	req, err := store.RequestWithPlan("rm", []string{"-f", target}, root, plan, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	server := New(manager, filesystem.New(pol))
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	gateResult, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "approval_gate_status", Arguments: map[string]any{"approval_id": req.ID}})
	if err != nil || gateResult.IsError {
		t.Fatalf("gate result=%+v err=%v", gateResult, err)
	}
	raw, err := json.Marshal(gateResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var gate approval.GateStatus
	if err := json.Unmarshal(raw, &gate); err != nil {
		t.Fatal(err)
	}
	if gate.State != approval.GateHold || gate.Allowed || gate.DedupKey != "approval:"+req.ID {
		t.Fatalf("unexpected gate: %+v", gate)
	}
	before, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec_start", Arguments: map[string]any{"idempotency_key": "expired-approval", "command": "rm", "args": []string{"-f", target}, "workdir": root, "plan_hash": plan}})
	if err == nil && !result.IsError {
		t.Fatalf("unanswered approval authorized mutation: %+v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("target changed: %q err=%v", data, err)
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(before) {
		t.Fatalf("rejected operation changed state entries: before=%v after=%v", before, entries)
	}
	for i := range before {
		if before[i].Name() != entries[i].Name() {
			t.Fatalf("job state changed: before=%v after=%v", before, entries)
		}
	}
}
