package jobs

import (
	"context"
	"mcpshka/internal/audit"
	"mcpshka/internal/failure"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoCacheMaintenance(t *testing.T) {
	m, _ := testManager(t)
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := audit.New(logPath, 8192)
	if err != nil {
		t.Fatal(err)
	}
	m.audit = logger
	cache := filepath.Join(m.home, ".cache", "go-build")
	bucket := filepath.Join(cache, "00")
	if err := os.MkdirAll(bucket, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(bucket, strings.Repeat("0", 64)+"-d")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate((1 << 30) + 1); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.jobs["active"] = &Job{ID: "active", State: Running, Command: "go"}
	m.mu.Unlock()
	if err := m.maintainGoCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("cleaned active cache: %v", err)
	}
	m.mu.Lock()
	delete(m.jobs, "active")
	m.mu.Unlock()
	if err := m.maintainGoCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(raw), `"result":"cleaned"`) {
		t.Fatalf("missing cleanup audit: %s %v", raw, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("cache not cleaned: %v", err)
	}
}

func TestGoCacheMaintenanceRejectsSymlink(t *testing.T) {
	m, _ := testManager(t)
	if err := os.MkdirAll(filepath.Join(m.home, ".cache"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(m.home, ".cache", "go-build")); err != nil {
		t.Fatal(err)
	}
	if err := m.maintainGoCache(context.Background()); err == nil {
		t.Fatal("accepted cache symlink")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
}

func TestGoCacheMaintenanceAdmission(t *testing.T) {
	m, work := testManager(t)
	defer m.Close(time.Second)
	m.mu.Lock()
	m.cacheMaintenance = true
	m.mu.Unlock()
	if _, err := m.Start("sleep", []string{"1"}, work); !failure.IsNoEffect(err) || failure.Details(err).Code != failure.GoCacheMaintenance || failure.Details(err).RetryAfterSeconds <= 0 {
		t.Fatalf("expected retryable no-effect cache refusal: %v", err)
	}
	source := filepath.Join(work, "source")
	if out, err := exec.Command("git", "init", "-q", source).CombinedOutput(); err != nil {
		t.Fatalf("init fixture: %s: %v", out, err)
	}
	dest := filepath.Join(work, "clone")
	job, err := m.Start("git", []string{"clone", "--no-local", source, dest}, work)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitForTerminal(t, m, job.ID); got.State != Done {
		t.Fatalf("clone: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
}

func TestGoCacheExpiresOldEntriesButKeepsRecent(t *testing.T) {
	m, _ := testManager(t)
	cache := filepath.Join(m.home, ".cache", "go-build", "00")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(cache, strings.Repeat("1", 64)+"-d")
	recent := filepath.Join(cache, strings.Repeat("2", 64)+"-d")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, []byte("cache"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := m.maintainGoCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old cache remains", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent cache removed", err)
	}
}
