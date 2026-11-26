package mcpserver

import (
	"context"
	"testing"
	"time"

	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
)

// S5.CM.5: async code_run returns a job at once and the script finishes in it.
func TestCodeRunAsyncReturnsJob(t *testing.T) {
	r := codeRegistry(t)
	p := policy.Default()
	p.AllowedWorkdirs = []string{t.TempDir()}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	r.receipts = manager
	out, err := r.codeRun(context.Background(), CodeRunInput{Script: "return mcp.call('echo_double', {n: 21}).double", Async: true, TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out.Result.(map[string]any)
	id, _ := m["job"].(string)
	if id == "" {
		t.Fatalf("no job: %+v", out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := manager.Get(id); err == nil && s.State == jobs.Done {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	s, _ := manager.Get(id)
	t.Fatalf("async code_run did not finish: %+v", s)
}
