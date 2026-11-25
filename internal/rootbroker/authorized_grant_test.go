package rootbroker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MCP-112: after Face ID the challenge file is gone; a repeated finish or a
// reloaded page must read «authorized», not «no such file or directory».
func TestAuthorizedGrantAnswersRepeatedFinish(t *testing.T) {
	dir := t.TempDir()
	path := func(scope, id string) string { return filepath.Join(dir, scope+"-"+id+".json") }
	id := strings.Repeat("a", 32)
	if _, ok := authorizedGrantAt(path, id); ok {
		t.Fatal("no grant yet")
	}
	if err := os.WriteFile(path(approvedScope, id), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, ok := authorizedGrantAt(path, id)
	if !ok || got.State != "authorized" || got.Scope != approvedScope || got.TaskID != id {
		t.Fatalf("grant %+v %v", got, ok)
	}
	if _, ok := authorizedGrantAt(path, "../x"); ok {
		t.Fatal("invalid id accepted")
	}
}
