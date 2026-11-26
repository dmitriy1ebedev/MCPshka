package jobs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestScopeDisappearanceChecksBothCgroupParents(t *testing.T) {
	for _, parent := range []string{"/system.slice/", "/mcpshka.slice/mcpshka-heavy.slice/"} {
		read := func(path string) ([]byte, error) {
			if strings.Contains(path, parent) {
				return []byte("populated 1\n"), nil
			}
			return nil, os.ErrNotExist
		}
		if gone, err := scopeCgroupsGone("mcpshka-job-a.scope", read); gone || err == nil {
			t.Fatal("lost descendants in", parent)
		}
	}
	if gone, err := scopeCgroupsGone("mcpshka-job-a.scope", func(string) ([]byte, error) { return []byte("populated 0\n"), nil }); !gone || err != nil {
		t.Fatal(gone, err)
	}
}

func TestObserveScopeAcrossSliceMigration(t *testing.T) {
	old := cgroupRoot
	t.Cleanup(func() { cgroupRoot = old })
	for _, parent := range []string{"system.slice", "mcpshka.slice/mcpshka-heavy.slice"} {
		cgroupRoot = t.TempDir()
		for _, unit := range []string{"mcpshka-job-migration.scope", "mcpshka-executor-" + strings.Repeat("a", 32) + ".service"} {
			dir := filepath.Join(cgroupRoot, parent, unit)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("42"), 0600); err != nil {
				t.Fatal(err)
			}
			var obs ProcessObservation
			if strings.HasSuffix(unit, ".scope") {
				obs = observeScope(unit)
			} else {
				obs = observeExecutorUnit(unit)
			}
			if !obs.Alive || obs.RSSBytes != 42 {
				t.Fatalf("%s: %+v", parent, obs)
			}
		}
	}
}
