package missiontools

import (
	"mcpshka/internal/agents"
	"mcpshka/internal/mission"
	"os"
	"path/filepath"
	"testing"
)

func TestMissionUnsafeRecordsAndBlockers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	if err := os.Symlink("/dev/zero", path); err != nil {
		t.Fatal(err)
	}
	if mission.ReadJSON(path, new(agents.Task)) == nil {
		t.Fatal("followed symlink")
	}
	if len(missionBlockers("lease busy; quota; Face ID required; CI failed; worker stopped")) != 5 {
		t.Fatal("missing blocker classes")
	}
	if len(missionBlockers("")) != 0 {
		t.Fatal("invented blocker")
	}
}
