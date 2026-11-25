package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcpshka/internal/executor"
)

func writeExport(t *testing.T, base, project, head string, at time.Time, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	m := PipelineManifest{Schema: "mcpshka.pipeline/v1", Project: project, HEAD: head, Tree: "t", ProfileDigest: "d", Stage: "package", State: "PASS", FinishedAt: at,
		Artifacts: []executor.PipelineArtifact{{Path: "out/app.ipk", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body))}}}
	dir := filepath.Join(base, pipelineIdentity(m))
	m.ManifestPath = filepath.Join(dir, "manifest.json")
	os.MkdirAll(filepath.Join(dir, "out"), 0o700)
	os.WriteFile(filepath.Join(dir, "out/app.ipk"), []byte(body), 0o600)
	raw, _ := json.Marshal(m)
	os.WriteFile(m.ManifestPath, raw, 0o600)
	return filepath.Join(dir, "out/app.ipk")
}

func TestPackagedArtifactTakesVerifiedExportOfHead(t *testing.T) {
	base, now := t.TempDir(), time.Now()
	writeExport(t, base, "/r", "h1", now, "old")
	want := writeExport(t, base, "/r", "h1", now.Add(time.Minute), "new")
	writeExport(t, base, "/r", "h0", now.Add(time.Hour), "other head")
	writeExport(t, base, "/x", "h1", now.Add(time.Hour), "other project")
	got, m, err := PackagedArtifact(base, "/r", "h1", "out/app.ipk")
	if err != nil || got != want || m.HEAD != "h1" {
		t.Fatalf("%q %v", got, err)
	}
	if _, _, err := PackagedArtifact(base, "/r", "h2", "out/app.ipk"); err == nil {
		t.Fatal("export of another HEAD accepted")
	}
	if _, _, err := PackagedArtifact(base, "/r", "h1", "out/none.ipk"); err == nil {
		t.Fatal("missing artifact accepted")
	}
	os.WriteFile(want, []byte("tampered"), 0o600)
	if _, _, err := PackagedArtifact(base, "/r", "h1", "out/app.ipk"); err == nil {
		t.Fatal("changed artifact accepted")
	}
}
