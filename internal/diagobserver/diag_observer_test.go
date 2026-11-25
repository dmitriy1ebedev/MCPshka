package diagobserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiagObserverReadsOnlyTheFixedJSON(t *testing.T) {
	if _, err := Run(context.Background(), []string{"/etc/shadow"}); err == nil {
		t.Fatal("arguments must be refused")
	}
	dir := t.TempDir()
	old := diagObserverPath
	defer func() { diagObserverPath = old }()
	diagObserverPath = filepath.Join(dir, "status.json")
	os.WriteFile(diagObserverPath, []byte(`{"ok":true}`), 0600)
	raw, err := Run(context.Background(), nil)
	if os.Geteuid() != 0 {
		if err == nil {
			t.Fatal("a file not owned by root must be refused")
		}
		return
	}
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatalf("%s %v", raw, err)
	}
	link := filepath.Join(dir, "link.json")
	os.Symlink(diagObserverPath, link)
	diagObserverPath = link
	if _, err := Run(context.Background(), nil); err == nil {
		t.Fatal("symlink must be refused")
	}
}

func TestDiagObserverViewArgs(t *testing.T) {
	for _, args := range [][]string{{"baseline"}, {"shell", "45m"}, {"baseline", "45m;"}, {"incidents", "0m"}} {
		if _, err := Run(context.Background(), args); err == nil {
			t.Fatalf("%v must be refused", args)
		}
	}
}
