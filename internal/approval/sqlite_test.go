package approval

import (
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSQLiteApprovalOneTimeAcrossStores(t *testing.T) {
	root := t.TempDir()
	if _, err := statestore.Open(filepath.Join(root, "state.db")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "state.mode"), []byte("sqlite-v1\n"), 0600)
	s := New(filepath.Join(root, "approvals"))
	r, err := s.Request("go", []string{"version"}, root, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Grant(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := New(filepath.Join(root, "approvals"))
			if _, err := s.Consume(token, "go", []string{"version"}, root); err == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("approval winners=%d", winners.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "approvals", r.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("JSON authority mirror")
	}
}
