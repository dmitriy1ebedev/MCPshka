package freellm

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/issues"
	"mcpshka/internal/llmgateway"
)

func TestMCP239PrepareRejectsCreateOnlyOverExistingEmptyFile(t *testing.T) {
	c := &Conveyor{Dir: t.TempDir()}
	c.Compare = func(_ context.Context, request string) (llmgateway.Comparison, error) {
		reply := `{"edits":[{"path":"cmd/demo/main.go","old":"","new":"package main"}]}`
		if strings.Contains(request, "plan repository patch steps") {
			reply = `{"steps":[{"paths":["cmd/demo/main.go"]}]}`
		}
		return llmgateway.Comparison{Replies: []llmgateway.Reply{{Model: "one", Text: reply}}}, nil
	}
	released := false
	c.Invoke = func(_ context.Context, name string, in map[string]any) (any, error) {
		switch name {
		case "project_worktree":
			return map[string]any{"worktree": map[string]any{"path": "/work/fix", "head": "base"}}, nil
		case "project_lease_acquire":
			return map[string]any{"token": "fixture"}, nil
		case "fs_read":
			return map[string]any{"text": "", "sha256": "existing-empty-file-sha"}, nil
		case "project_lease_release":
			released = true
			return map[string]any{}, nil
		default:
			t.Fatalf("existing empty file must never be written: %s", name)
			return nil, nil
		}
	}
	_, err := c.prepare(context.Background(), Fix{Card: "MCP-239", Branch: "fix/mcp239", At: time.Now()}, issues.Issue{Project: "lane"}, []string{"cmd/demo/main.go"})
	if err == nil || !strings.Contains(err.Error(), "create-only proposal targets existing file") || !released {
		t.Fatalf("empty existing file overwrite not blocked/released: %v, %v", err, released)
	}
}

func TestPrepareNewFileUsesCreateOnlyWrite(t *testing.T) {
	c := &Conveyor{Dir: t.TempDir()}
	patchCalls := 0
	c.Compare = func(_ context.Context, request string) (llmgateway.Comparison, error) {
		text := `{"edits":[{"path":"cmd/demo/main.go","old":"","new":"package main"}]}`
		if strings.Contains(request, "plan repository patch steps") {
			text = `{"steps":[{"paths":["cmd/demo/main.go"]}]}`
		} else {
			patchCalls++
			if !strings.Contains(request, "VALIDATION_ERROR") {
				text = `{"edits":[{"path":"cmd/imaginary/main.go","old":"","new":"package main"}]}`
			} else if !strings.Contains(request, "not in the current step file allowlist") {
				t.Fatal("missing validation feedback", request)
			}
		}
		return llmgateway.Comparison{Replies: []llmgateway.Reply{{Model: "one", Text: text}}}, nil
	}
	written := false
	c.Invoke = func(_ context.Context, name string, in map[string]any) (any, error) {
		switch name {
		case "project_worktree":
			return map[string]any{"worktree": map[string]any{"path": "/work/fix", "head": "base"}}, nil
		case "project_lease_acquire":
			return map[string]any{"token": "fixture"}, nil
		case "fs_read":
			return nil, errors.New("file absent")
		case "fs_mkdir":
			if in["path"] != "/work/fix/cmd/demo" {
				t.Fatal(in["path"])
			}
			return nil, nil
		case "fs_write":
			if in["expected_sha256"] != "" || in["path"] != "/work/fix/cmd/demo/main.go" {
				t.Fatal("not create-only")
			}
			raw, err := base64.StdEncoding.DecodeString(in["content_base64"].(string))
			if err != nil || string(raw) != "package main" {
				t.Fatal(string(raw), err)
			}
			written = true
			return nil, nil
		case "git_diff":
			return map[string]any{"text": "+package main"}, nil
		case "project_commit":
			return map[string]any{"commit": strings.Repeat("a", 40)}, nil
		case "project_test":
			return map[string]any{"job": map[string]any{"id": "fixture-test"}}, nil
		case "job_wait":
			return map[string]any{"status": map[string]any{"state": "done", "exit_code": 0}}, nil
		case "project_lease_release":
			return nil, nil
		default:
			t.Fatal("unexpected tool", name)
			return nil, nil
		}
	}
	f, err := c.prepare(context.Background(), Fix{Card: "MCP-209", Branch: "fix/mcp-209", Mode: "manual", At: time.Now()}, issues.Issue{Project: "lane"}, []string{"cmd/demo/main.go"})
	if err != nil || !written || f.State != "green" || patchCalls != 2 {
		t.Fatal(f.State, written, patchCalls, err)
	}
	// Failure to save the credential must still return the acquired lease.
	if err := os.Mkdir(filepath.Join(c.Dir, "MCP-210.lease-token"), 0700); err != nil {
		t.Fatal(err)
	}
	invoke, released := c.Invoke, false
	c.Invoke = func(ctx context.Context, name string, in map[string]any) (any, error) {
		if name == "project_lease_release" {
			released = true
		}
		return invoke(ctx, name, in)
	}
	_, err = c.prepare(context.Background(), Fix{Card: "MCP-210", Branch: "fix/mcp-210", Mode: "manual", At: time.Now()}, issues.Issue{Project: "lane"}, []string{"cmd/demo/main.go"})
	if err == nil || !released {
		t.Fatal("credential write failure leaked its lease", err, released)
	}
}
