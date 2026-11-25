package agents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHealthProviderQuotaAndAuthorizationAreIndependent(t *testing.T) {
	old := taskDir
	taskDir = t.TempDir()
	defer func() { taskDir = old }()
	now := time.Now().UTC()
	if err := saveTask(Task{ID: "aaaaaaaaaaaaaaaa", Agent: Codex, State: "quota", FinishedAt: now, ResetAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got := Health(context.Background(), now)
	if got[0].Identity.Quota.State != "EXHAUSTED" || got[0].Identity.Quota.ResetAt == nil || got[0].Quota.State != "degraded" || got[0].Authorization.State != "unknown" || got[1].State != "unknown" {
		t.Fatalf("quota or missing auth looks healthy: %+v", got)
	}
	if err := saveTask(Task{ID: "aaaaaaaaaaaaaaaa", Agent: Codex, State: "done", FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	got = Health(context.Background(), now)
	if got[0].Authorization.State != "ok" || got[0].Quota.State != "unknown" || got[0].State == "ok" {
		t.Fatalf("success invented quota: %+v", got)
	}
	got = Health(context.Background(), now.Add(6*time.Minute))
	if got[0].Authorization.State != "stale" {
		t.Fatalf("old auth looks fresh: %+v", got)
	}
}

func TestHealthProviderAuthFailureIsNotQuotaFailure(t *testing.T) {
	old := taskDir
	taskDir = t.TempDir()
	defer func() { taskDir = old }()
	now := time.Now().UTC()
	if err := saveTask(Task{ID: "bbbbbbbbbbbbbbbb", Agent: Claude, State: "failed", FinishedAt: now, Tail: "Authentication failed: token expired"}); err != nil {
		t.Fatal(err)
	}
	got := Health(context.Background(), now)
	if got[1].Authorization.State != "degraded" || got[1].Quota.State != "unknown" || got[1].Authorization.NextSafeAction == nil {
		t.Fatalf("auth conflated with quota: %+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "token expired") {
		t.Fatal("raw task output leaked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got = Health(ctx, now)
	if got[1].State != "unknown" {
		t.Fatal("canceled read looked healthy")
	}
}
