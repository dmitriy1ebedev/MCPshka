package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"strings"
	"testing"
	"time"
)

func TestCompactJobStatus(t *testing.T) {
	s := jobs.Snapshot{ID: "test", State: jobs.Running, BootID: "boot", StartTicks: 42, TimeoutSeconds: 1800, OutputLimitBytes: jobs.DefaultJobOutputLimit, MemoryLimitBytes: jobs.DefaultMemoryLimit, CPUTimeLimitSeconds: 1800, ProcessLimit: jobs.DefaultProcessLimit, CPUQuotaPercent: jobs.DefaultCPUQuotaPercent}
	out, err := jobStatusView(s, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"boot_id", "start_ticks", "finished_at", "started_at", "last_output_at", "reconciled_at", "timeout_seconds", "output_limit_bytes", "memory_limit_bytes", "cpu_time_limit_seconds", "process_limit", "cpu_quota_percent"} {
		if _, ok := out[key]; ok {
			t.Errorf("compact contains %s", key)
		}
	}
	s.TimeoutSeconds = 17
	out, err = jobStatusView(s, false)
	if err != nil || out["timeout_seconds"] != float64(17) {
		t.Fatalf("custom limit lost: %v %v", out, err)
	}
	out, err = jobStatusView(s, true)
	if err != nil || out["boot_id"] != "boot" {
		t.Fatalf("verbose lost details: %v %v", out, err)
	}
}

func TestJobWaitTool(t *testing.T) {
	root := t.TempDir()
	m, err := jobs.New(t.TempDir(), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	client := uploadClient(t, m, "wait-test", "operator")
	receipt, err := m.StoreReceipt("fixture", nil, root, strings.Repeat("x", 4096)+"END")
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "job_wait", Arguments: map[string]any{"job_id": receipt.ID, "timeout_seconds": 1}})
	if err != nil || res.IsError {
		t.Fatalf("wait: %v %+v", err, res)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out JobWaitResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status["id"] != receipt.ID || len(out.Stdout)+len(out.Stderr) > 2048 || !strings.HasSuffix(out.Stdout, "END") {
		t.Fatalf("bad wait result: %+v", out)
	}
	res, err = client.CallTool(context.Background(), &mcp.CallToolParams{Name: "job_wait", Arguments: map[string]any{"job_id": receipt.ID, "timeout_seconds": -1}})
	if err == nil && !res.IsError {
		t.Fatal("accepted timeout -1")
	}
	// MCP-117: a long wait is clamped to 45 s, not refused as a blocker.
	if res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "job_wait", Arguments: map[string]any{"job_id": receipt.ID, "timeout_seconds": 900}}); err != nil || res.IsError {
		t.Fatalf("refused timeout 900: %v %+v", err, res)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitJob(ctx, m, receipt.ID, 1); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestJobWaitRunningAndDeadline(t *testing.T) {
	root := t.TempDir()
	m, err := jobs.New(t.TempDir(), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	started, err := m.Start("sleep", []string{"0.2"}, root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := waitJob(context.Background(), m, started.ID, 2)
	if err != nil || out.Status["state"] != "done" {
		t.Fatalf("did not wait for exit: %+v %v", out, err)
	}
	started, err = m.Start("sleep", []string{"5"}, root)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	out, err = waitJob(context.Background(), m, started.ID, 0)
	if err != nil || out.Status["state"] != "running" || time.Since(before) > time.Second {
		t.Fatalf("zero timeout blocked: %+v %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := waitJob(ctx, m, started.ID, 2); err != context.DeadlineExceeded {
		t.Fatalf("context deadline: %v", err)
	}
}
