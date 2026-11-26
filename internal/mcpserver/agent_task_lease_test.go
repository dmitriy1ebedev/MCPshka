package mcpserver

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/agents"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
)

// The cancel path must release through the root broker (the server cannot
// own the lease SQLite) and pass the pinned plan from preview to release.
func TestReleaseTaskLeaseUsesBrokerPreviewThenRelease(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "app")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	p := &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}}
	r := &toolRegistry{leases: projectlease.New(filepath.Join(base, "leases"), p)}
	task := agents.Task{ID: "abc", Path: repo, State: "cancelled"}
	if got := r.releaseTaskLease(context.Background(), "s", "o", task); got != "not held" {
		t.Fatal("free lease:", got)
	}
	if _, err := r.leases.Acquire(repo, "agent-task:abc"); err != nil {
		t.Fatal(err)
	}
	var calls []string
	old := staleLeaseBroker
	defer func() { staleLeaseBroker = old }()
	staleLeaseBroker = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0]+":"+args[len(args)-1])
		if args[0] == "preview-stale-lease" {
			return []byte(`{"path":"` + repo + `"}`), nil
		}
		return []byte(`{}`), nil
	}
	if got := r.releaseTaskLease(context.Background(), "s", "o", task); got != "released" {
		t.Fatal(got)
	}
	if len(calls) != 2 || calls[0] != "preview-stale-lease:"+repo || !strings.Contains(calls[1], `"path":"`+repo+`"`) {
		t.Fatal(calls)
	}
	staleLeaseBroker = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("holder alive") }
	if got := r.releaseTaskLease(context.Background(), "s", "o", task); got != "kept: holder alive" {
		t.Fatal(got)
	}
	task.State = "running"
	if got := r.releaseTaskLease(context.Background(), "s", "o", task); got != "not held" {
		t.Fatal("running task lease touched:", got)
	}
}
