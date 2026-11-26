package mcpserver

import (
	"context"
	"strings"
	"testing"
)

func TestExecutorCanaryRejectsBootstrapAndOverridesBeforeBroker(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, in := range []ExecStartInput{
		{Args: []string{"executor-canary", "prepare", id, id}, IdempotencyKey: "test"},
		{Args: []string{"executor-canary", "launch", "../root", id}, IdempotencyKey: "test"},
		{Args: []string{"executor-canary", "launch", id, id}, IdempotencyKey: "test", Env: map[string]string{"PATH": "/evil"}},
		{Args: []string{"executor-canary", "launch", id, id}, IdempotencyKey: "test", ApprovalToken: "override"},
		{Args: []string{"executor-canary", "launch", id, id}},
	} {
		if _, e := executorCanaryCompat(context.Background(), nil, in); e == nil {
			t.Fatalf("unsafe input accepted: %+v", in)
		}
	}
}
