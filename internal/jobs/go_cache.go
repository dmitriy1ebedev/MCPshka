package jobs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"mcpshka/internal/audit"
	"mcpshka/internal/policy"
)

var goCacheEntry = regexp.MustCompile(`^[0-9a-f]{64}-[ad]$`)

// The cache is shared across projects, so maintenance excludes all native jobs.
func (m *Manager) maintainGoCache(ctx context.Context) (result error) {
	m.mu.Lock()
	if m.closing || m.cacheMaintenance || m.starting != 0 {
		m.mu.Unlock()
		return nil
	}
	for _, j := range m.jobs {
		if pendingWriter(j) {
			m.mu.Unlock()
			return nil
		}
	}
	m.cacheMaintenance = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.cacheMaintenance = false; m.mu.Unlock() }()
	defer func() {
		if result != nil {
			m.log(audit.Event{Type: "go_cache_maintenance", Result: result.Error()})
		}
	}()
	cache := filepath.Join(m.home, ".cache", "go-build")
	real, err := filepath.EvalSymlinks(cache)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if real != cache {
		return errors.New("Go cache symlink: maintenance skipped")
	}
	scan, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var size int64
	expired := []string{}
	entries := 0
	err = filepath.WalkDir(cache, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := scan.Err(); err != nil {
			return err
		}
		entries++
		if entries > 100000 {
			return errors.New("Go cache scan entry budget exceeded")
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("Go cache contains symlink: maintenance skipped")
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if goCacheEntry.MatchString(d.Name()) && time.Since(info.ModTime()) > 7*24*time.Hour && len(expired) < 128 {
				expired = append(expired, path)
			}
			size += info.Size()
			if size > 1<<30 {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range expired {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return err
		}
		info, err := root.Lstat(filepath.Base(path))
		if err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > 7*24*time.Hour {
			err = root.Remove(filepath.Base(path))
		}
		root.Close()
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if size <= 1<<30 {
		return nil
	}
	m.log(audit.Event{Type: "go_cache_maintenance", Result: "started", Details: map[string]any{"observed_bytes": size, "threshold_bytes": 1 << 30}})
	clean, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	// Go reads its telemetry mode from a file, not GOTELEMETRY: without "off"
	// it leaves a detached child writing under HOME after the clean returns
	// (BUG-20261024-86).
	if err := disableGoTelemetry(m.home); err != nil {
		return err
	}
	cmd := exec.CommandContext(clean, "go", "clean", "-cache")
	cmd.Dir = m.home
	cmd.Env = append(policy.MinimalEnv(m.home), "GOENV=off", "GOTOOLCHAIN=local")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return err
	}
	m.log(audit.Event{Type: "go_cache_maintenance", Result: "cleaned", Details: map[string]any{"observed_bytes": size}})
	return nil
}

func disableGoTelemetry(home string) error {
	dir := filepath.Join(home, ".config", "go", "telemetry")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "mode"), []byte("off"), 0600)
}
