package roadmaprun

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunnerRequiresExplicitOwnerAgentSet(t *testing.T) {
	withStateDir(t)
	calls := 0
	cfg := Config{ID: "no-consent", Path: "/p", Goal: "repair", MaxPhases: 1, Budget: time.Hour,
		RunAgent: func(context.Context, string, string, string) (string, error) { calls++; return "TASK_DONE", nil },
		Test: func(context.Context, string, string, string, func(string) error) (string, string, string, error) {
			return "job", "head", "", nil
		}}
	_, err := Run(context.Background(), cfg, "")
	if err == nil || !strings.Contains(err.Error(), "explicit owner") || calls != 0 {
		t.Fatalf("implicit delegation occurred: calls=%d error=%v", calls, err)
	}
}
