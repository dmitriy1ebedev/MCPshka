package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/statestore"
)

func TestIntegrationQueueMCPColdAdmissionAndCancel(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Queue Test")
	git("config", "user.email", "queue@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "ROADMAP.md"), []byte("## P7 — Queue\n- [ ] integration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "ROADMAP.md")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-b", "feature")
	git("commit", "--allow-empty", "-m", "candidate")
	commit := git("rev-parse", "HEAD")
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scan, err := roadmapscan.Load(filepath.Join(root, "ROADMAP.md"), "")
	if err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	startClient := func(owner string, scopes ...string) (*mcp.ClientSession, func()) {
		t.Helper()
		manager, err := jobs.New(filepath.Join(state, "jobs"), p)
		if err != nil {
			t.Fatal(err)
		}
		st, ct := mcp.NewInMemoryTransports()
		if len(scopes) == 0 {
			scopes = []string{"*"}
		}
		ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: owner, Role: "operator", Scopes: scopes})
		ss, err := New(manager, filesystem.New(p)).Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "queue-canary", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		return cs, func() { cs.Close(); ss.Close(); manager.Close(time.Second) }
	}
	cs, closeFirst := startClient("controller-a")
	args := map[string]any{"path": root, "id": "candidate-1", "branch": "feature", "commit": commit, "target_branch": "main", "target_head": base, "roadmap_sha256": scan.SHA256, "scope": "P7", "idempotency_key": "enqueue-1", "owned_branch_id": strings.Repeat("a", 32)}
	for i := 0; i < 2; i++ {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "integration_enqueue", Arguments: args})
		if err != nil || res.IsError {
			closeFirst()
			t.Fatalf("admit/retry: %+v %v", res, err)
		}
	}
	closeFirst()
	cold, closeCold := startClient("controller-a")
	defer closeCold()
	res, err := cold.CallTool(context.Background(), &mcp.CallToolParams{Name: "integration_status", Arguments: map[string]any{"path": root, "id": "candidate-1"}})
	if err != nil || res.IsError {
		t.Fatalf("cold status: %+v %v", res, err)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var status integrationqueue.RuntimeStatus
	if err := json.Unmarshal(raw, &status); err != nil || !status.Found || status.Pending != 1 {
		t.Fatalf("durable status: %s %v", raw, err)
	}
	if status.Item == nil || status.Item.Request.OwnedBranchID != strings.Repeat("a", 32) {
		t.Fatal("MCP dropped resource binding")
	}
	cancel := map[string]any{"path": root, "id": "candidate-1", "commit": commit, "idempotency_key": "cancel-1"}
	res, err = cold.CallTool(context.Background(), &mcp.CallToolParams{Name: "integration_cancel", Arguments: cancel})
	if err != nil || res.IsError {
		t.Fatalf("cancel: %+v %v", res, err)
	}
	foreign, closeForeign := startClient("controller-b")
	defer closeForeign()
	foreignResult, foreignErr := foreign.CallTool(context.Background(), &mcp.CallToolParams{Name: "integration_cancel", Arguments: cancel})
	if foreignErr == nil && !foreignResult.IsError {
		t.Fatal("foreign principal replayed cached cancellation success")
	}
	foreignResult, foreignErr = foreign.CallTool(context.Background(), &mcp.CallToolParams{Name: "integration_enqueue", Arguments: args})
	if foreignErr == nil && !foreignResult.IsError {
		t.Fatal("foreign principal replayed cached admission success")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	compatArgs := map[string]any{"command": "mcpshka", "workdir": root, "args": []string{"integration", "enqueue", string(encoded)}, "idempotency_key": "compat-enqueue-1"}
	compat, err := cold.CallTool(context.Background(), &mcp.CallToolParams{Name: "exec_start", Arguments: compatArgs})
	if err != nil || compat.IsError {
		detail, _ := json.Marshal(compat)
		t.Fatalf("compat admission: %s %v", detail, err)
	}
	compatRaw, err := json.Marshal(compat.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var receipt jobs.Snapshot
	if err = json.Unmarshal(compatRaw, &receipt); err != nil || receipt.State != jobs.Done {
		t.Fatalf("compat did not return durable in-process receipt: %s %v", compatRaw, err)
	}
	restricted, closeRestricted := startClient("controller-a", "exec_start")
	defer closeRestricted()
	restrictedResult, restrictedErr := restricted.CallTool(context.Background(), &mcp.CallToolParams{Name: "exec_start", Arguments: compatArgs})
	if restrictedErr == nil && !restrictedResult.IsError {
		t.Fatal("same owner replayed integration receipt without native action scope")
	}
	foreignResult, foreignErr = foreign.CallTool(context.Background(), &mcp.CallToolParams{Name: "exec_start", Arguments: compatArgs})
	if foreignErr == nil && !foreignResult.IsError {
		t.Fatal("foreign principal replayed cached compat admission")
	}
	status, err = integrationqueue.NewRuntime(mustQueueDB(t, state), p).Status(context.Background(), root, "candidate-1")
	if err != nil || status.Item.State != integrationqueue.Cancelled || git("rev-parse", "main") != base {
		t.Fatalf("cancel mutated Git or lost pin: %+v %v", status, err)
	}
}

func mustQueueDB(t *testing.T, state string) *statestore.DB {
	t.Helper()
	db, err := statestore.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}
