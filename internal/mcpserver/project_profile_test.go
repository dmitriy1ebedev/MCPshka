package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/statestore"
)

func TestWorkflowCheckpointStatusAcrossAIReconnection(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	stateRoot := t.TempDir()
	db, err := statestore.Open(filepath.Join(stateRoot, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	first, created, err := projectprofile.StartCheckpoint(db, "p7-mcp-canary", hash)
	if err != nil || !created {
		t.Fatalf("start: %v %v", created, err)
	}
	manager, err := jobs.New(filepath.Join(stateRoot, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	server := New(manager, filesystem.New(p))
	for i := 0; i < 2; i++ {
		client := mcp.NewClient(&mcp.Implementation{Name: "handoff-ai", Version: "1"}, nil)
		st, ct := mcp.NewInMemoryTransports()
		ss, err := server.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := client.Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workflow_checkpoint_status", Arguments: map[string]any{"workflow_id": "p7-mcp-canary"}})
		if err != nil || res.IsError {
			t.Fatalf("MCP status: %+v %v", res, err)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var got projectprofile.Checkpoint
		if err := json.Unmarshal(raw, &got); err != nil || got != first {
			t.Fatalf("lost handoff checkpoint: %+v %v", got, err)
		}
		_ = cs.Close()
		_ = ss.Close()
	}
}

func TestWorkflowCheckpointStartOverMCP(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/p7start\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".mcpshka"), 0700); err != nil {
		t.Fatal(err)
	}
	profile := projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "start-test", Match: projectprofile.Match{Module: "example.test/p7start", Markers: []string{"go.mod"}}, Workflows: map[string]projectprofile.Workflow{"check": {Command: "printf", Args: []string{"ok"}, TimeoutSeconds: 10, OutputLimitBytes: 1024}}}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcpshka", "profile.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	state := t.TempDir()
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	server := New(manager, filesystem.New(p))
	client := mcp.NewClient(&mcp.Implementation{Name: "ai-one", Version: "1"}, nil)
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
	args := map[string]any{"workflow_id": "p7-start-canary", "path": root, "workflow": "check", "idempotency_key": "p7-start-canary-key"}
	for i := 0; i < 2; i++ {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workflow_checkpoint_start", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("start %d: %+v %v", i, res, err)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var cp projectprofile.Checkpoint
		if err := json.Unmarshal(raw, &cp); err != nil || cp.WorkflowID != "p7-start-canary" || len(cp.PlanFingerprint) != 64 || cp.Revision != 1 {
			t.Fatalf("bad pinned start: %+v %v", cp, err)
		}
	}
}

func TestProjectProfileAndRunTools(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/profiled\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".mcpshka")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	profile := projectprofile.Profile{
		Schema: projectprofile.SchemaV1,
		Name:   "test-profile",
		Match:  projectprofile.Match{Module: "example.test/profiled", Markers: []string{"go.mod"}},
		Workflows: map[string]projectprofile.Workflow{
			"check": {
				Command: "printf", Args: []string{"profile-ok"},
				TimeoutSeconds: 20, OutputLimitBytes: 4096,
			},
		},
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

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

	profileRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "project_profile",
		Arguments: map[string]any{"path": root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profileRes.IsError {
		t.Fatalf("project_profile returned error: %+v", profileRes.Content)
	}
	profileJSON, err := json.Marshal(profileRes.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var resolved projectprofile.Resolution
	if err := json.Unmarshal(profileJSON, &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.Profile.Name != "test-profile" || resolved.Source != "repository" {
		t.Fatalf("unexpected profile: %+v", resolved)
	}

	runRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "project_run",
		Arguments: map[string]any{
			"path":               root,
			"workflow":           "check",
			"timeout_seconds":    10,
			"output_limit_bytes": 2048,
			"idempotency_key":    "profile-run-test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runRes.IsError {
		t.Fatalf("project_run returned error: %+v", runRes.Content)
	}
	runJSON, err := json.Marshal(runRes.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var run ProjectRunResult
	if err := json.Unmarshal(runJSON, &run); err != nil {
		t.Fatal(err)
	}
	if run.Plan.Profile != "test-profile" || run.Plan.Workflow != "check" || run.Job.ID == "" {
		t.Fatalf("unexpected run result: %+v", run)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := manager.Get(run.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snap.State == jobs.Done {
			out, err := manager.Read(run.Job.ID, "stdout", 0, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if out.Text != "profile-ok" {
				t.Fatalf("unexpected output %q", out.Text)
			}
			if snap.OutputLimitBytes != 2048 {
				t.Fatalf("request did not tighten profile output cap: %+v", snap)
			}
			return
		}
		if snap.State == jobs.Failed || snap.State == jobs.Cancelled {
			t.Fatalf("profile job ended %s: %+v", snap.State, snap)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("profile workflow did not finish")
}

func TestManagedWorkflowHandoffAcrossMCPSessions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/handoff\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".mcpshka"), 0700); err != nil {
		t.Fatal(err)
	}
	pipePath := filepath.Join(root, "worker.fifo")
	if err := syscall.Mkfifo(pipePath, 0600); err != nil {
		t.Fatal(err)
	}
	releaseWorker := func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			fd, err := syscall.Open(pipePath, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err == nil {
				if err := syscall.Close(fd); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != syscall.ENXIO || time.Now().After(deadline) {
				t.Fatalf("worker did not open its gate: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	profile := projectprofile.Profile{
		Schema: projectprofile.SchemaV1, Name: "handoff",
		Match: projectprofile.Match{Module: "example.test/handoff", Markers: []string{"go.mod"}},
		Workflows: map[string]projectprofile.Workflow{
			"check": {Command: "cat", Args: []string{pipePath}, TimeoutSeconds: 10, OutputLimitBytes: 2048},
		},
	}
	b, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcpshka", "profile.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	stateRoot := t.TempDir()
	if _, err := statestore.Open(filepath.Join(stateRoot, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(filepath.Join(stateRoot, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(time.Second) })
	server := New(manager, filesystem.New(p))
	client1 := mcp.NewClient(&mcp.Implementation{Name: "ai-one", Version: "1"}, nil)
	st1, ct1 := mcp.NewInMemoryTransports()
	ss1, err := server.Connect(ctx, st1, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs1, err := client1.Connect(ctx, ct1, nil)
	if err != nil {
		_ = ss1.Close()
		t.Fatal(err)
	}
	args := map[string]any{
		"path": root, "workflow": "check", "managed": true,
		"timeout_seconds": 5, "output_limit_bytes": 1024,
		"idempotency_key": "p7-handoff-same-identity",
	}
	res, err := cs1.CallTool(ctx, &mcp.CallToolParams{Name: "project_run", Arguments: args})
	if err != nil || res.IsError {
		payload, _ := json.Marshal(res.Content)
		t.Fatalf("first session failed to submit: %s %v", payload, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var first ProjectRunResult
	if err := json.Unmarshal(raw, &first); err != nil || first.Job.ID == "" {
		t.Fatalf("invalid job receipt: %+v, %v", first, err)
	}
	_ = cs1.Close()
	_ = ss1.Close()

	client2 := mcp.NewClient(&mcp.Implementation{Name: "ai-two", Version: "1"}, nil)
	st2, ct2 := mcp.NewInMemoryTransports()
	ss2, err := server.Connect(ctx, st2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss2.Close()
	cs2, err := client2.Connect(ctx, ct2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs2.Close()
	retry, err := cs2.CallTool(ctx, &mcp.CallToolParams{Name: "project_run", Arguments: args})
	if err != nil || retry.IsError {
		t.Fatalf("handoff replay failed: %+v %v", retry, err)
	}
	raw, _ = json.Marshal(retry.StructuredContent)
	var same ProjectRunResult
	if err := json.Unmarshal(raw, &same); err != nil || same.Job.ID != first.Job.ID {
		t.Fatalf("handoff created a duplicate worker: first=%s next=%s err=%v", first.Job.ID, same.Job.ID, err)
	}
	other := map[string]any{
		"path": root, "workflow": "check", "managed": true,
		"timeout_seconds": 5, "output_limit_bytes": 1024,
		"idempotency_key": "p7-handoff-competing-writer",
	}
	blocked, err := cs2.CallTool(ctx, &mcp.CallToolParams{Name: "project_run", Arguments: other})
	if err != nil || !blocked.IsError {
		t.Fatalf("competing writer not fenced: %+v %v", blocked, err)
	}
	// PROJECT_BUSY is a no-effect refusal (MCP-136): the same call may be
	// repeated, but while the project is busy the repeat is fenced again and
	// never starts a second worker.
	busyReplay, err := cs2.CallTool(ctx, &mcp.CallToolParams{Name: "project_run", Arguments: other})
	if err != nil || !busyReplay.IsError {
		t.Fatalf("repeat of a fenced writer was admitted while busy: %+v %v", busyReplay, err)
	}
	// Release the worker only after the competing writer has been fenced.
	releaseWorker()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := manager.Get(first.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == jobs.Done {
			break
		}
		if s.State != jobs.Running {
			t.Fatalf("first job terminated unexpectedly: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	finished, err := manager.Get(first.Job.ID)
	if err != nil || finished.State != jobs.Done {
		t.Fatalf("handoff job did not complete: %+v %v", finished, err)
	}
	leases, deployments := managedServices(manager)
	rec, err := leases.RecoverManaged(root, manager, deployments)
	if err != nil || !rec.Released {
		t.Fatalf("lease was not recovered after actual job exit: %+v %v", rec, err)
	}
	nextArgs := map[string]any{
		"path": root, "workflow": "check", "managed": true,
		"timeout_seconds": 5, "output_limit_bytes": 1024,
		"idempotency_key": "p7-handoff-new-authorized-attempt",
	}
	next, err := cs2.CallTool(ctx, &mcp.CallToolParams{Name: "project_run", Arguments: nextArgs})
	if err != nil || next.IsError {
		payload, _ := json.Marshal(next.Content)
		t.Fatalf("new writer not admitted after verified release: %s %v", payload, err)
	}
	raw, _ = json.Marshal(next.StructuredContent)
	var second ProjectRunResult
	if err := json.Unmarshal(raw, &second); err != nil || second.Job.ID == first.Job.ID {
		t.Fatalf("new writer must have unique job identity: first=%s next=%s err=%v", first.Job.ID, second.Job.ID, err)
	}
	releaseWorker()
	// Do not leave background jobs running when the test fixture is removed.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := manager.Get(second.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == jobs.Done {
			return
		}
		if s.State != jobs.Running {
			t.Fatalf("second writer failed: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("second writer never completed")
}

// MCP-188: a registered profile stage the host policy refuses (python3)
// names the sealed pipeline route instead of a bare POLICY_DENIED.
func TestProjectRunDeniedStageNamesPipeline(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/ipk\n\ngo 1.26\n"), 0600)
	os.Mkdir(filepath.Join(root, ".mcpshka"), 0700)
	profile := projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "ipk", Match: projectprofile.Match{Module: "example.test/ipk", Markers: []string{"go.mod"}},
		Workflows: map[string]projectprofile.Workflow{"package": {Command: "python3", Args: []string{"webos/build_ipk.py"}, TimeoutSeconds: 20, OutputLimitBytes: 4096}}}
	data, _ := json.Marshal(profile)
	os.WriteFile(filepath.Join(root, ".mcpshka", "profile.json"), data, 0600)
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}, AllowedCommandClasses: map[policy.CommandClass]bool{policy.ClassReadOnly: true, policy.ClassMutating: true}}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	server := New(manager, filesystem.New(p))
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
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_run", Arguments: map[string]any{"path": root, "workflow": "package", "idempotency_key": "mcp-188-run"}})
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !res.IsError || !strings.Contains(text, "project_pipeline path="+root+" stages=[package]") || !strings.Contains(text, "not allowed by policy") {
		t.Fatalf("want the sealed pipeline route, got %q", text)
	}
}
