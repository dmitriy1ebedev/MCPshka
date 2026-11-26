package verify

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the host's real home-priority switch out of every test.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "verify-home")
	if err != nil {
		panic(err)
	}
	homeOffFile = filepath.Join(dir, "home-priority-off")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
