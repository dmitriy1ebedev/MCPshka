package freellm

import (
	"context"
	"strings"
	"testing"

	"mcpshka/internal/issues"
	"mcpshka/internal/llmgateway"
)

func TestProjectsPrioritiesAndMultiStepPlans(t *testing.T) {
	for _, project := range []string{"mcpshka", "lane", "diag", "/root/repos/lane"} {
		name, root := projectRoot(project)
		if name == "" || root != "/root/repos/"+name {
			t.Fatal(project, root)
		}
	}
	if _, root := projectRoot("lane/../other"); root != "" {
		t.Fatal(root)
	}
	cards := []issues.Summary{{ID: "1", Title: "single", Priority: "P2"}, {ID: "2", Title: "repeat", Priority: "P2"}, {ID: "3", Title: "repeat", Priority: "P2"}, {ID: "4", Title: "urgent", Priority: "P1"}}
	prioritize(cards)
	if cards[0].ID != "4" || cards[1].ID != "2" || cards[2].ID != "3" {
		t.Fatal(cards)
	}
	c := &Conveyor{Compare: func(_ context.Context, request string) (llmgateway.Comparison, error) {
		if !strings.Contains(request, "plan repository patch steps") {
			t.Fatal(request)
		}
		return llmgateway.Comparison{Replies: []llmgateway.Reply{{Text: `{"steps":[{"paths":["cmd/app/main.go"]},{"paths":["laned/router.py","docs/router.md"]}]}`}}}, nil
	}}
	steps, err := c.plan(context.Background(), Fix{Root: "/root/repos/lane"}, issues.Issue{}, []string{"cmd/app/main.go", "laned/router.py", "docs/router.md"})
	if err != nil || len(steps) != 2 || len(steps[1]) != 2 {
		t.Fatal(steps, err)
	}
	c.Compare = func(context.Context, string) (llmgateway.Comparison, error) {
		return llmgateway.Comparison{Replies: []llmgateway.Reply{{Text: `{"steps":[{"paths":["../outside"]}]}`}}}, nil
	}
	if _, err := c.plan(context.Background(), Fix{}, issues.Issue{}, []string{"x.go"}); err == nil {
		t.Fatal("unsafe plan accepted")
	}
	for _, path := range []string{"cmd/lane-go/router.go", "laned/dns_ops.py", "internal/rootbroker/health.go"} {
		if !safeFixPath(path) {
			t.Fatal(path)
		}
	}
}

func TestPlanRejectsInventedPathThenRetriesWithAllowlist(t *testing.T) {
	calls := 0
	c := &Conveyor{Compare: func(_ context.Context, prompt string) (llmgateway.Comparison, error) {
		calls++
		if !strings.Contains(prompt, "available_paths") || !strings.Contains(prompt, "cmd/lane-go/router.go") {
			t.Fatal("missing trusted file candidates", prompt)
		}
		if calls == 1 {
			return llmgateway.Comparison{Replies: []llmgateway.Reply{{Text: `{"steps":[{"paths":["wrapper/lane.sh"]}]}`}}}, nil
		}
		if !strings.Contains(prompt, "unknown/unsafe planned path") {
			t.Fatal("retry lacks precise validation feedback", prompt)
		}
		return llmgateway.Comparison{Replies: []llmgateway.Reply{{Text: `{"steps":[{"paths":["cmd/lane-go/router.go"]}]}`}}}, nil
	}}
	steps, err := c.plan(context.Background(), Fix{Root: "/root/repos/lane"}, issues.Issue{}, []string{"cmd/lane-go/router.go"})
	if err != nil || calls != 2 || len(steps) != 1 || len(steps[0]) != 1 || steps[0][0] != "cmd/lane-go/router.go" {
		t.Fatal("invented plan was not rejected and corrected", steps, calls, err)
	}
}
