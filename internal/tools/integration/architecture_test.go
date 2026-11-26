package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/distributedci"
)

func TestShipRejectsArchitectureBeforeCI(t *testing.T) {
	for _, found := range []bool{false, true} {
		in := IntegrationShipInput{Path: "/root/repos/mcpshka", Branch: "candidate", Commit: strings.Repeat("a", 40), AdminSessionID: "admin", Evidence: "auto"}
		env := Env{Architecture: func(_ context.Context, repo, target, commit string) error {
			if repo != in.Path || target != "main" || commit != in.Commit {
				t.Fatalf("wrong preflight identity: %s %s %s", repo, target, commit)
			}
			return errors.New("archlint: internal/executor exceeds its line cap")
		}}
		env.Head = func(context.Context, string, string) (string, error) { return strings.Repeat("b", 40), nil }
		env.Roadmap = func(string) (string, error) { return strings.Repeat("c", 64), nil }
		call := func(_ context.Context, tool string, _ map[string]any) (map[string]any, error) {
			if tool == "integration_enqueue" && !found {
				return map[string]any{}, nil
			}
			if tool != "integration_status" {
				t.Fatalf("called %s after preflight failure", tool)
			}
			return map[string]any{"found": found, "item": map[string]any{"request": map[string]any{"commit": in.Commit}}}, nil
		}
		out, err := Step(context.Background(), env, in, call)
		if err != nil || out.Stage != "ARCHLINT_FAILED" || !strings.Contains(out.Detail, "internal/executor") {
			t.Fatalf("preflight refusal = %+v, %v", out, err)
		}
	}
}

func TestShipAllowsStartedItemsToReconcile(t *testing.T) {
	in := IntegrationShipInput{Path: "/root/repos/mcpshka", Branch: "candidate", Commit: strings.Repeat("a", 40), AdminSessionID: "admin", Evidence: "module"}
	for _, state := range []string{"integrating", "integrated"} {
		stop := errors.New("existing permit")
		env := Env{Now: time.Now, Architecture: func(context.Context, string, string, string) error {
			t.Fatal("archlint preflight blocked reconciliation")
			return nil
		}}
		_, err := Step(context.Background(), env, in, func(_ context.Context, tool string, _ map[string]any) (map[string]any, error) {
			if tool == "integration_status" {
				return map[string]any{"found": true, "item": map[string]any{"state": state, "request": map[string]any{"commit": in.Commit}}}, nil
			}
			if tool != "integration_test_prepare" {
				t.Fatalf("unexpected tool %s", tool)
			}
			return nil, stop
		})
		if !errors.Is(err, stop) {
			t.Fatalf("%s: %v", state, err)
		}
	}
}

func TestShipReportsSealedArchlintFailure(t *testing.T) {
	in := IntegrationShipInput{Path: "/root/repos/mcpshka", Branch: "candidate", Commit: strings.Repeat("a", 40), AdminSessionID: "admin", Evidence: "module"}
	out, err := Step(context.Background(), Env{Now: time.Now}, in, func(_ context.Context, tool string, _ map[string]any) (map[string]any, error) {
		switch tool {
		case "integration_status":
			return map[string]any{"found": true, "item": map[string]any{"request": map[string]any{"commit": in.Commit}}}, nil
		case "integration_test_prepare":
			return map[string]any{"state": "SEALED"}, nil
		case "exec_start":
			return map[string]any{"id": "job"}, nil
		case "job_output":
			raw, _ := json.Marshal(map[string]any{"state": "FENCED", "result": map[string]any{"state": "FAILED", "output": distributedci.ArchlintFailed + "\n"}})
			return map[string]any{"text": string(raw)}, nil
		default:
			return nil, errors.New("unexpected tool " + tool)
		}
	})
	if err != nil || out.Stage != "TEST_FAILED" || !strings.Contains(out.Detail, "remaining tests were skipped") {
		t.Fatalf("sealed refusal = %+v, %v", out, err)
	}
}
