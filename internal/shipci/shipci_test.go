package shipci

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fake answers forge_ci with a job whose output is the reply for provider/mode.
func fake(replies map[string]map[string]any, calls *[]string) Gate {
	var last string
	return Gate{Commit: strings.Repeat("a", 40), Key: func(s string) string { return s }, Bucket: "1",
		Call: func(_ context.Context, tool string, args map[string]any) (map[string]any, error) {
			switch tool {
			case "forge_ci":
				last = args["provider"].(string) + "/" + args["mode"].(string)
				*calls = append(*calls, last)
				return map[string]any{"id": "j"}, nil
			case "job_output":
				raw, _ := json.Marshal(replies[last])
				return map[string]any{"stdout": string(raw)}, nil
			}
			return map[string]any{}, nil
		}}
}

func TestAutoPicksFirstPassingSite(t *testing.T) {
	var calls []string
	ev, stage, _, _ := fake(map[string]map[string]any{"gitverse/status": {"state": "observed_pass"}}, &calls).Auto(context.Background())
	if ev != "gitverse" || stage != "" || strings.Contains(strings.Join(calls, ","), "github") {
		t.Fatalf("healthy GitVerse: %q %q %v", ev, stage, calls)
	}
	calls = nil
	envFail := map[string]any{"state": "HOLD", "runs": []any{1}, "failure": map[string]any{"failure_class": "ENV_FAILURE"}}
	ev, stage, _, _ = fake(map[string]map[string]any{"gitverse/status": envFail, "github/status": {"state": "observed_pass"}}, &calls).Auto(context.Background())
	if ev != "ci" || stage != "" || !strings.Contains(strings.Join(calls, ","), "gitverse/rerun") {
		t.Fatalf("GitVerse site down → rerun + GitHub: %q %q %v", ev, stage, calls)
	}
	calls = nil
	ev, stage, _, _ = fake(map[string]map[string]any{"gitverse/status": envFail, "github/status": {"state": "running"}}, &calls).Auto(context.Background())
	if ev != "" || stage != "CI_FALLBACK" {
		t.Fatalf("waiting on fallback: %q %q", ev, stage)
	}
}

func TestAutoFallsBackWhenNoRunner(t *testing.T) {
	var calls []string
	waiting := map[string]any{"state": "HOLD", "runs": []any{map[string]any{"id": 1, "status": "waiting"}}}
	ev, stage, _, _ := fake(map[string]map[string]any{"gitverse/status": waiting, "github/status": {"state": "observed_pass"}}, &calls).Auto(context.Background())
	if ev != "ci" || stage != "" || strings.Contains(strings.Join(calls, ","), "gitverse/rerun") {
		t.Fatalf("no GitVerse runner → GitHub without rerun: %q %q %v", ev, stage, calls)
	}
	calls = nil
	running := map[string]any{"state": "HOLD", "runs": []any{map[string]any{"id": 1, "status": "running"}}}
	ev, stage, _, _ = fake(map[string]map[string]any{"gitverse/status": running}, &calls).Auto(context.Background())
	if ev != "" || stage != "CI_RUNNING" || strings.Contains(strings.Join(calls, ","), "github") {
		t.Fatalf("a running GitVerse job keeps GitHub idle: %q %q %v", ev, stage, calls)
	}
}

// MCP-271: GitVerse can return HOLD with a nonempty cancelled run and a
// diagnosis outside the older "failure" field. This is provider failure,
// NOT evidence that the source tests failed or that CI is still running.
func TestAutoFallsBackWhenGitVersePlatformCancelled(t *testing.T) {
	var calls []string
	cancelled := map[string]any{
		"state": "HOLD",
		"diagnosis": map[string]any{
			"code":   "CI_PLATFORM_CANCELLED",
			"reason": "Provider cancelled execution; quota is not verified",
		},
		"runs": []any{map[string]any{"id": 2185586, "status": "cancelled"}},
	}
	g := fake(map[string]map[string]any{
		"gitverse/status": cancelled,
		"github/status":   {"state": "observed_pass"},
	}, &calls)
	ev, stage, detail, _ := g.Auto(context.Background())
	if ev != "ci" || stage != "" || detail != "" {
		t.Fatalf("cancelled GitVerse must use verified GitHub evidence: %q %q %q %v", ev, stage, detail, calls)
	}
	seen := strings.Join(calls, ",")
	if !strings.Contains(seen, "gitverse/rerun") || !strings.Contains(seen, "github/status") {
		t.Fatalf("cancelled provider must attempt one rerun and check independent CI: %v", calls)
	}
}

func TestGitVersePlatformCancellationIsNotCodeFailure(t *testing.T) {
	var calls []string
	g := fake(map[string]map[string]any{
		"gitverse/status": {
			"state":     "HOLD",
			"diagnosis": map[string]any{"code": "CI_PLATFORM_CANCELLED"},
			"runs":      []any{map[string]any{"status": "cancelled"}},
		},
	}, &calls)
	stage, detail, _, providerFailure := g.GitVerse(context.Background())
	if stage != "CI_FAILED" || !providerFailure || !strings.Contains(detail, "cancelled") {
		t.Fatalf("cancelled GitVerse run is a provider failure, not CI_RUNNING or source test failure: %q %q %v %v", stage, detail, providerFailure, calls)
	}
}

func TestAutoStopsOnCodeFailure(t *testing.T) {
	var calls []string
	codeFail := map[string]any{"state": "HOLD", "runs": []any{1}, "failure": map[string]any{"failure_class": "TEST_FAILURE"}}
	ev, stage, _, _ := fake(map[string]map[string]any{"gitverse/status": codeFail}, &calls).Auto(context.Background())
	if ev != "" || stage != "CI_FAILED" || strings.Contains(strings.Join(calls, ","), "github") || strings.Contains(strings.Join(calls, ","), "rerun") {
		t.Fatalf("code failure must stop without fallback: %q %q %v", ev, stage, calls)
	}
}

func TestLeaseHeldIsItsOwnStage(t *testing.T) {
	var calls []string
	g := Gate{Commit: strings.Repeat("a", 40), Key: func(s string) string { return s }, Bucket: "1",
		Call: func(_ context.Context, tool string, args map[string]any) (map[string]any, error) {
			calls = append(calls, tool+":"+args["provider"].(string))
			return nil, errors.New("forge_ci: [OWNER_BUSY] interactive or legacy project lease held: HOLD")
		}}
	ev, stage, _, next := g.Auto(context.Background())
	if ev != "" || stage != "LEASE_HELD" || !strings.Contains(next, "project_lease_release") || strings.Contains(strings.Join(calls, ","), "github") {
		t.Fatalf("held lease: %q %q %q %v", ev, stage, next, calls)
	}
}

func TestGitHubAccountBlockIsNotACodeFailure(t *testing.T) {
	var calls []string
	blocked := map[string]any{"state": "observed_fail", "diagnosis": map[string]any{"code": "CI_ACCOUNT_BLOCKED"}}
	stage, _, next := fake(map[string]map[string]any{"github/status": blocked}, &calls).GitHub(context.Background())
	if stage != "CI_ACCOUNT_BLOCKED" || !strings.Contains(next, "evidence=module") {
		t.Fatalf("account block: %q %q", stage, next)
	}
	stage, _, _ = fake(map[string]map[string]any{"github/status": {"state": "observed_fail"}}, &calls).GitHub(context.Background())
	if stage != "CI_FAILED" {
		t.Fatalf("code failure: %q", stage)
	}
}

// Three cancellations in a row stop republish/rerun and file one card.
func TestGitVerseStuckStopsRetries(t *testing.T) {
	var calls []string
	c := map[string]any{"status": "cancelled"}
	g := fake(map[string]map[string]any{
		"gitverse/status": {"state": "HOLD", "diagnosis": map[string]any{"code": "CI_PLATFORM_CANCELLED"}, "runs": []any{c, c, c}},
		"github/status":   {"state": "observed_pass"},
	}, &calls)
	ev, stage, _, _ := g.Auto(context.Background())
	seen := strings.Join(calls, ",")
	if ev != "ci" || stage != "" || strings.Contains(seen, "gitverse/rerun") || strings.Contains(seen, "gitverse/publish") {
		t.Fatalf("stuck GitVerse must not be rerun, GitHub decides: %q %q %v", ev, stage, calls)
	}
	stage, _, _, env := g.GitVerse(context.Background())
	if stage != "CI_STUCK" || env {
		t.Fatalf("stage %q env %v", stage, env)
	}
}
