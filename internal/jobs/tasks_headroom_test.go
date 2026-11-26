package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTasksExhaustionRefusesAdmission(t *testing.T) {
	root := t.TempDir()
	oldRoot, oldSelf := cgroupRoot, selfCgroupFile
	cgroupRoot, selfCgroupFile = root, filepath.Join(root, "self-cgroup")
	t.Cleanup(func() { cgroupRoot, selfCgroupFile = oldRoot, oldSelf })
	dir := filepath.Join(root, "system.slice", "mcpshka.service")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, v string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(selfCgroupFile, []byte("0::/system.slice/mcpshka.service\n"), 0o644)
	write("pids.max", "100")
	write("pids.current", "40")
	if err := checkTasksHeadroom(); err != nil {
		t.Fatalf("40/100 must admit: %v", err)
	}
	write("pids.current", "95")
	if err := checkTasksHeadroom(); !errors.Is(err, ErrTasksExhausted) {
		t.Fatalf("95/100 must refuse: %v", err)
	}
	write("pids.max", "max")
	if err := checkTasksHeadroom(); err != nil {
		t.Fatalf("unlimited cgroup must admit: %v", err)
	}
	_ = os.WriteFile(selfCgroupFile, []byte("0::/../etc\n"), 0o644)
	if _, _, ok := TasksHeadroom(); ok {
		t.Fatal("path traversal accepted")
	}
}
