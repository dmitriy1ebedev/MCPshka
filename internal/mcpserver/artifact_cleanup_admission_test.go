package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/artifactcleanup"
	"mcpshka/internal/gitops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/projectops"
)

func TestArtifactCleanupLiveJobPreservesFiles(t *testing.T) {
	m, leases, held := yieldFixture(t)
	defer m.Close(time.Second)
	path := filepath.Join(held.Path, "artifact")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	var cleanup tooldef.Def
	projectops.Register(func(d tooldef.Def) {
		if d.Tool.Name == "project_artifact_cleanup" {
			cleanup = d
		}
	}, projectops.Deps{Manager: m, Leases: leases, Git: gitops.New(m.Policy()), Store: idempotency.New(t.TempDir()), Digest: func(s string) string { return "digest" }})
	in := projectops.ArtifactCleanupInput{Path: held.Path, Mode: "preview", LeaseToken: held.Token, Files: []artifactcleanup.File{{Path: "artifact"}}, IdempotencyKey: "preview"}
	_, v, err := cleanup.Handler(context.Background(), nil, in)
	if err != nil {
		t.Fatal(err)
	}
	preview := v.(artifactcleanup.Report)
	in.Mode, in.IdempotencyKey, in.Files = "apply", "apply", []artifactcleanup.File{preview.Files[0].File}
	job, err := m.StartLLM(context.Background(), func(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Cancel(job.ID)
	if _, _, err := cleanup.Handler(context.Background(), nil, in); err == nil {
		t.Fatal("cleanup admitted live job")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatal("live artifact changed", err)
	}
}
