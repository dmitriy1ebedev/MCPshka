package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/projectlease"
)

func TestPendingPublishReconcilesOriginalGitEffect(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(args, err, string(raw))
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "--allow-empty", "-qm", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-qb", "feature")
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("preserve result"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "result.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit", "-qm", "candidate")
	candidate := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "pending-publish", Version: "1"}, nil), store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["integration_recovery_status"] = func(context.Context, map[string]any) (any, error) {
		return map[string]any{"state": "RELEASED", "candidate": candidate}, nil
	}
	calls, effects := 0, 0
	addTool(r, &mcp.Tool{Name: "integration_publish_main"}, func(ctx context.Context, _ *mcp.CallToolRequest, in IntegrationPublishInput) (*mcp.CallToolResult, IntegrationPublishResult, error) {
		calls++
		state, err := integrationqueue.PublishFastForward(ctx, root, "main", base, candidate)
		out := IntegrationPublishResult{State: state, Path: root, Branch: "main", Candidate: candidate}
		if err != nil {
			return nil, out, err
		}
		if state != "ALREADY_PUBLISHED" {
			effects++
		}
		if calls == 1 {
			return nil, out, errors.New("response lost after publication; outcome UNKNOWN")
		}
		return nil, out, nil
	})
	args, _ := object(IntegrationPublishInput{PermitID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 32), Workdir: root, IdempotencyKey: "lost-publish"})
	for attempt := 0; attempt < 3; attempt++ {
		_, out, err := r.invokers["integration_publish_main"](context.Background(), args)
		if attempt == 0 {
			if err == nil {
				t.Fatal("fixture did not lose response")
			}
			continue
		}
		if err != nil {
			t.Fatalf("attempt %d cannot reconcile original publication: %v", attempt, err)
		}
		if out.(IntegrationPublishResult).State != "ALREADY_PUBLISHED" {
			t.Fatal("original effect not observed", out)
		}
	}
	if calls != 2 || effects != 1 || git("rev-parse", "HEAD") != candidate {
		t.Fatal("publication replayed", calls, effects)
	}
}

func TestPendingReplayRefusalCannotForgetOriginalEffect(t *testing.T) {
	ctx := context.Background()
	store := idempotency.New(t.TempDir())
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "pending-replay", Version: "1"}, nil), store: store, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["project_lease_admin_recovery_status"] = func(context.Context, map[string]any) (any, error) { return map[string]any{"state": "observed"}, nil }
	calls := 0
	addTool(r, &mcp.Tool{Name: "project_lease_admin_recover"}, func(context.Context, *mcp.CallToolRequest, AdminLegacyLeaseRecoverInput) (*mcp.CallToolResult, projectlease.AcquireResult, error) {
		calls++
		if calls == 1 {
			return nil, projectlease.AcquireResult{}, errors.New("recovery effect UNKNOWN")
		}
		return nil, projectlease.AcquireResult{}, failure.Rejected("FIXTURE_RETRY_BLOCKED", "test", "this retry was refused before launch", nil)
	})
	args, _ := object(AdminLegacyLeaseRecoverInput{Path: t.TempDir(), SessionID: "session", IdempotencyKey: "pending-recovery"})
	if _, _, err := r.invokers["project_lease_admin_recover"](ctx, args); err == nil {
		t.Fatal("fixture did not lose response")
	}
	_, _, err := r.invokers["project_lease_admin_recover"](ctx, args)
	if err == nil || failure.IsNoEffect(err) {
		t.Error("retry refusal hid the original possible effect", err)
	}
	key := mutationReceiptKey(ctx, "project_lease_admin_recover", "pending-recovery", args)
	hash := sha256.Sum256([]byte(key))
	status, lookupErr := store.Lookup("contract:" + hex.EncodeToString(hash[:]))
	if lookupErr != nil || status.State != idempotency.Pending {
		t.Fatal("original UNKNOWN receipt was erased", status, lookupErr)
	}
	if calls != 2 {
		t.Fatal("unexpected attempts", calls)
	}
}

func TestInvokerAppliesSchemaDefaultsWithoutChangingCaller(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional_default", true: "required_stays_required"}[required], func(t *testing.T) {
			ctx := context.Background()
			r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "defaults", Version: "1"}, nil), readers: map[string]probeHandler{}, tools: map[string]bool{}}
			schema := map[string]any{"type": "object", "properties": map[string]any{"choice": map[string]any{"type": "string", "enum": []string{"accepted"}, "default": "accepted"}}, "additionalProperties": false}
			if required {
				schema["required"] = []string{"choice"}
			}
			addTool(r, &mcp.Tool{Name: "fixture_defaults", InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
				return nil, in, nil
			})
			st, ct := mcp.NewInMemoryTransports()
			server, err := r.server.Connect(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "defaults-test", Version: "1"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			direct, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "fixture_defaults", Arguments: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{}
			_, out, viaErr := r.invokers["fixture_defaults"](ctx, args)
			if direct.IsError != required || (viaErr != nil) != required {
				t.Fatalf("SDK/gateway disagreement: required=%v direct=%+v gateway=%v", required, direct, viaErr)
			}
			if !required && (out.(map[string]any)["choice"] != "accepted" || direct.StructuredContent.(map[string]any)["choice"] != "accepted") {
				t.Fatal("optional default missing", out, direct)
			}
			if len(args) != 0 {
				t.Fatal("caller arguments mutated", args)
			}
		})
	}
}

func TestBeforeAuditRefusalDoesNotStrandFreshReceipt(t *testing.T) {
	ctx := context.Background()
	store := idempotency.New(t.TempDir())
	logDir := filepath.Join(t.TempDir(), "audit")
	logger, err := audit.New(filepath.Join(logDir, "tool.jsonl"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(logDir); err != nil {
		t.Fatal(err)
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "audit-refusal", Version: "1"}, nil), store: store, audit: logger, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["project_lease_admin_recovery_status"] = func(context.Context, map[string]any) (any, error) {
		return map[string]any{"state": "observed"}, nil
	}
	addTool(r, &mcp.Tool{Name: "project_lease_admin_recover"}, func(context.Context, *mcp.CallToolRequest, AdminLegacyLeaseRecoverInput) (*mcp.CallToolResult, projectlease.AcquireResult, error) {
		t.Fatal("handler ran without the before-audit record")
		return nil, projectlease.AcquireResult{}, nil
	})
	args, err := object(AdminLegacyLeaseRecoverInput{Path: t.TempDir(), SessionID: "session", IdempotencyKey: "unstarted-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.invokers["project_lease_admin_recover"](ctx, args)
	if !failure.IsNoEffect(err) {
		t.Fatalf("missing verified no-effect refusal: %v", err)
	}
	key := mutationReceiptKey(ctx, "project_lease_admin_recover", "unstarted-recovery", args)
	hash := sha256.Sum256([]byte(key))
	if s, lookupErr := store.Lookup("contract:" + hex.EncodeToString(hash[:])); !errors.Is(lookupErr, os.ErrNotExist) {
		t.Fatalf("fresh unstarted receipt was left behind: %+v, %v", s, lookupErr)
	}
}
