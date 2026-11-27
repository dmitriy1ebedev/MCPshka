package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// S5.TIDY.2: the checkpoint archive file never lands in the index; records
// an older import made from it are closed.
func TestImportSkipsCheckpointArchive(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	dir := t.TempDir()
	if _, _, err := s.Write(ctx, Record{Kind: "archive", Project: "p", Title: "старый блок", Body: "история выпуска", Source: "claude-memory:demo-checkpoint-archive-to-20261124#2"}); err != nil {
		t.Fatal(err)
	}
	arch := "---\nname: demo-checkpoint-archive-to-20261124\ntype: project\n---\n\n## Блок\nархивный текст про выпуск\n"
	if err := os.WriteFile(filepath.Join(dir, "demo-checkpoint-archive-to-20261124.md"), []byte(arch), 0600); err != nil {
		t.Fatal(err)
	}
	rep, err := s.ImportMarkdown(ctx, dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Records != 0 || rep.Closed != 1 {
		t.Fatalf("archive must be skipped and the old record closed: %+v", rep)
	}
}
