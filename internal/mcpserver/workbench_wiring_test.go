package mcpserver

import (
	"path/filepath"
	"testing"

	"mcpshka/internal/workbenchruntime"
)

func TestServicesFromEnvBindSealedWorkbenchRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCPSHKA_STATE_DIR", filepath.Join(dir, "jobs"))
	t.Setenv("MCPSHKA_AUDIT_PATH", filepath.Join(dir, "audit.jsonl"))
	t.Setenv("MCPSHKA_JOB_SCOPES", "0")
	t.Setenv("MCPSHKA_DEFAULT_ROLE_PROFILE", "")
	t.Setenv("MCPSHKA_PROJECT_PROFILES", "")
	manager, _, _, stop, err := newServicesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	want := workbenchruntime.New(manager.Policy()).Name()
	if got := manager.ExternalRuntimeName(); got != want {
		t.Fatalf("external runtime = %q, want %q", got, want)
	}
}
