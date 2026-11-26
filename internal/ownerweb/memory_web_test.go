package ownerweb

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/memory"
)

func TestMemoryUploadInChunks(t *testing.T) {
	MemoryDir = t.TempDir()
	st, err := memory.Open(MemoryDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	text := "---\nname: kuler\ndescription: кулер включается автоматизацией Home Assistant\n---\n"
	for i := 0; i < 400; i++ {
		text += fmt.Sprintf("Замер %d: кулер на отметке %d градусов, вентилятор номер %d.\n\n", i, 40+i, i*7)
	}
	raw := []byte(text)
	total := (len(raw) + uploadChunk - 1) / uploadChunk
	id := "0123456789abcdef0123"
	var out map[string]any
	for i := 0; i < total; i++ {
		end := min((i+1)*uploadChunk, len(raw))
		if i == 1 {
			if _, err := memoryUpload(ctx, st, id, 2, total, "kuler.md", base64.StdEncoding.EncodeToString(raw[i*uploadChunk:end])); err == nil {
				t.Fatal("gap accepted")
			}
		}
		if out, err = memoryUpload(ctx, st, id, i, total, "../kuler.md", base64.StdEncoding.EncodeToString(raw[i*uploadChunk:end])); err != nil {
			t.Fatal(i, err)
		}
	}
	saved, _ := out["saved"].(string)
	if filepath.Dir(saved) != filepath.Join(MemoryDir, "memory-inbox") || !strings.HasSuffix(saved, "-kuler.md") {
		t.Fatalf("saved at %q", saved)
	}
	if got, _ := os.ReadFile(saved); string(got) != text {
		t.Fatal("file corrupted")
	}
	rep, ok := out["import"].(memory.ImportReport)
	if !ok || rep.Records < 2 || rep.Recall3 < 0.9 {
		t.Fatalf("%+v", out)
	}
	if _, err = memoryUpload(ctx, st, "BAD", 0, 1, "x", ""); err == nil {
		t.Fatal("bad id accepted")
	}
}
