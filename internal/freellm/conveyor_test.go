package freellm

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/llmgateway"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChooseQuorumIsIndependentAndBounded(t *testing.T) {
	files := map[string]string{"internal/demo/demo.go": "old"}
	raw := `{"edits":[{"path":"internal/demo/demo.go","old":"old","new":"fixed"}]}`
	result := llmgateway.Comparison{Replies: []llmgateway.Reply{{Model: "one", Text: raw}, {Model: "one", Text: raw}}}
	if _, q, e := Choose(result, files); e != nil || q {
		t.Fatal(q, e)
	}
	result.Replies[1].Model = "two"
	if _, q, e := Choose(result, files); e != nil || !q {
		t.Fatal(q, e)
	}
	for _, bad := range []string{"../outside.go", "internal/vault/secret.go", "internal/demo/../../outside.go"} {
		result.Replies[0].Text = strings.ReplaceAll(raw, "internal/demo/demo.go", bad)
		result.Replies = result.Replies[:1]
		if _, _, err := Choose(result, map[string]string{bad: "old"}); err == nil {
			t.Fatal("unsafe path", bad)
		}
	}
}
func TestConveyorManualGreenWaitsForReviewAndDoesNotReplay(t *testing.T) {
	ctx := context.Background()
	sends, tests, merges := 0, 0, 0
	roots := []string{}
	cards := []map[string]any{{"id": "MCP-209", "project": "mcpshka", "priority": "P2", "status": "open"}, {"id": "MCP-210", "project": "lane", "priority": "P1", "status": "open"}, {"id": "MCP-211", "project": "diag", "priority": "P1", "status": "open"}, {"id": "MCP-212", "project": "lane", "priority": "P3", "status": "open"}}
	c := &Conveyor{Dir: t.TempDir(), Now: func() time.Time { return time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC) }, Quiet: func() bool { return true }}
	c.Compare = func(_ context.Context, request string) (llmgateway.Comparison, error) {
		if strings.Contains(request, "plan repository patch steps") {
			return llmgateway.Comparison{Replies: []llmgateway.Reply{{Model: "one", Text: `{"steps":[{"paths":["internal/demo/demo.go"]}]}`}}}, nil
		}
		sends++
		return llmgateway.Comparison{Replies: []llmgateway.Reply{{Model: "one", Text: `{"edits":[{"path":"internal/demo/demo.go","old":"old","new":"fixed"}]}`}}}, nil
	}
	c.Invoke = func(_ context.Context, name string, in map[string]any) (any, error) {
		switch name {
		case "issue_list":
			return map[string]any{"issues": cards}, nil
		case "issue_get":
			for _, card := range cards {
				if card["id"] == in["id"] {
					return map[string]any{"id": card["id"], "project": card["project"], "body": "fix internal/demo/demo.go", "title": "fixture"}, nil
				}
			}
			return nil, errors.New("missing fixture card")
		case "fs_read":
			return map[string]any{"text": "old", "sha256": "hash"}, nil
		case "project_worktree":
			roots = append(roots, in["path"].(string))
			return map[string]any{"worktree": map[string]any{"path": "/work/fix", "head": "base"}}, nil
		case "project_lease_acquire":
			return map[string]any{"token": "private-fixture-token"}, nil
		case "fs_patch":
			return map[string]any{}, nil
		case "git_diff":
			return map[string]any{"text": "-old\n+fixed"}, nil
		case "project_commit":
			return map[string]any{"commit": strings.Repeat("a", 40)}, nil
		case "project_test":
			tests++
			return map[string]any{"job": map[string]any{"id": "test"}}, nil
		case "job_wait":
			return map[string]any{"status": map[string]any{"state": "done", "exit_code": 0}}, nil
		case "project_lease_release", "issue_update":
			return map[string]any{}, nil
		default:
			merges++
			return nil, errors.New("unexpected tool " + name)
		}
	}
	out, err := c.Run(ctx, "")
	if err != nil || len(out) != 4 || out[0].State != "green" || out[0].Diff == "" {
		t.Fatal(out, err)
	}
	_, err = c.Run(ctx, "manual")
	if err != nil || sends != 4 || tests != 4 || merges != 0 {
		t.Fatal(err, sends, tests, merges)
	}
	if out[0].Card != "MCP-210" || strings.Join(roots, ",") != "/root/repos/lane,/root/repos/diag,/root/repos/mcpshka,/root/repos/lane" {
		t.Fatal(out, roots)
	}
	public, _ := json.Marshal(out)
	if strings.Contains(string(public), "private-fixture-token") {
		t.Fatal("credential leak")
	}
	entries, _ := os.ReadDir(c.Dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lease-token") {
			t.Fatal("idle lease retained")
		}
	}
	rejected, err := c.Decide(ctx, []string{"MCP-209"}, "wrong fix", nil)
	if err != nil || rejected[0].State != "rejected" {
		t.Fatal(rejected, err)
	}
}
func TestLostTestDispatchReconcilesSameKey(t *testing.T) {
	c := &Conveyor{Dir: t.TempDir()}
	f := Fix{Card: "MCP-209", State: "dispatching_test", Path: "/work/fix", Packages: []string{"./internal/demo"}, At: time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)}
	if err := c.save(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Dir+"/MCP-209.lease-token", []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.Invoke = func(_ context.Context, name string, in map[string]any) (any, error) {
		switch name {
		case "project_test":
			calls++
			if in["idempotency_key"] != "freellm:MCP-209:20261118:test" || in["lease_token"] != "fixture" || len(in["packages"].([]string)) != 1 {
				t.Fatal(in)
			}
			return map[string]any{"job": map[string]any{"id": "original"}}, nil
		case "job_wait":
			return map[string]any{"status": map[string]any{"state": "done", "exit_code": 0}}, nil
		case "project_lease_release":
			return map[string]any{}, nil
		}
		t.Fatal(name)
		return nil, nil
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	all, err := c.List()
	if err != nil || len(all) != 1 || all[0].State != "green" || all[0].Test != "original" || calls != 1 {
		t.Fatal(all, err, calls)
	}
	if err := c.Refresh(context.Background()); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestConveyorDayBusyAutoAndForeignScope(t *testing.T) {
	c := &Conveyor{Dir: t.TempDir(), Now: func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }, Quiet: func() bool { return true }}
	if _, err := c.Run(context.Background(), "night"); err == nil || !strings.Contains(err.Error(), "NIGHT_NOT_QUIET") {
		t.Fatal("scheduled day run", err)
	}
	c.Now = func() time.Time { return time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC) }
	c.Quiet = func() bool { return false }
	if _, err := c.Run(context.Background(), "night"); err == nil || !strings.Contains(err.Error(), "NIGHT_NOT_QUIET") {
		t.Fatal("scheduled busy run", err)
	}
	if _, err := c.Run(context.Background(), "manual", "MCP-231"); err == nil || !strings.Contains(err.Error(), "NIGHT_NOT_QUIET") {
		t.Fatal("manual busy run must not start any work", err)
	}
	c.Quiet = func() bool { return true }
	c.Now = func() time.Time { return time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC) }
	c.Invoke = func(_ context.Context, name string, _ map[string]any) (any, error) {
		if name != "issue_list" {
			t.Fatalf("unexpected side effect %s", name)
		}
		return map[string]any{"issues": []any{}}, nil
	}
	if out, err := c.Run(context.Background(), "manual", "MCP-231"); err != nil || len(out) != 0 {
		t.Fatal("daytime manual run on quiet host", out, err)
	}
	t.Setenv("MCPSHKA_FREELLM_AUTO", "")
	if _, err := c.Run(context.Background(), "auto"); err == nil {
		t.Fatal("auto default")
	}
	t.Setenv("MCPSHKA_FREELLM_AUTO", "1")
	if _, err := c.Run(context.Background(), "auto"); err == nil {
		t.Fatal("automatic merge bypasses morning review")
	}
}
