package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func TestExecutorFenceRejectsInvalidAndOverrideInputsBeforeBroker(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, in := range []ExecStartInput{
		{Args: []string{"executor-fence", "../permit", id}, IdempotencyKey: "test"},
		{Args: []string{"executor-fence", id, id}, IdempotencyKey: "test", Env: map[string]string{"PATH": "/evil"}},
		{Args: []string{"executor-fence", id, id}, IdempotencyKey: "test", ApprovalToken: "caller-consent"},
		{Args: []string{"executor-fence", id, id}},
	} {
		if _, err := executorFenceCompat(context.Background(), nil, in); err == nil {
			t.Fatalf("unsafe fencing request: %+v", in)
		}
	}
}

func TestExecutorFenceCompatChecksNativeScopeBeforeCachedReceipt(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	db, err := statestore.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(time.Second)
	id := strings.Repeat("a", 32)
	in := ExecStartInput{Command: "mcpshka", Args: []string{"executor-fence", id, id}, Workdir: root, IdempotencyKey: "fence-retry"}
	input, err := object(in)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := idempotency.Fingerprint("exec_start", input)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(in.IdempotencyKey))
	key := "contract:" + hex.EncodeToString(sum[:])
	store := idempotency.NewWithDatabase(filepath.Join(state, "idempotency"), db)
	if _, err = store.Begin(key, "contract/exec_start", fp); err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(key, "contract/exec_start", fp, contractReceipt[jobs.Snapshot]{Output: jobs.Snapshot{ID: "fence-receipt", State: jobs.Done}}); err != nil {
		t.Fatal(err)
	}
	call := func(role string, scopes []string, name string, args any) (*mcp.CallToolResult, error) {
		t.Helper()
		ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "operator-a", Role: role, Scopes: scopes})
		st, ct := mcp.NewInMemoryTransports()
		ss, e := New(manager, filesystem.New(p)).Connect(ctx, st, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer ss.Close()
		cs, e := mcp.NewClient(&mcp.Implementation{Name: "fence-test", Version: "1"}, nil).Connect(ctx, ct, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer cs.Close()
		return cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	}
	res, err := call("operator", []string{"exec_start", "executor_fence"}, "exec_start", input)
	if err != nil || res.IsError {
		t.Fatalf("completed receipt retry: %+v %v", res, err)
	}
	res, err = call("operator", []string{"exec_start"}, "exec_start", input)
	if err == nil && !res.IsError {
		t.Fatal("reduced scope reused cached fence success")
	}
	res, err = call("reader", []string{"*"}, "executor_fence", map[string]any{"permit_id": id, "approval_id": id, "workdir": root, "idempotency_key": "native-retry"})
	if err == nil && !res.IsError {
		t.Fatal("reader admitted administrative mutation")
	}
}
