package forgeci

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProjectDistributedCIConfigurations(t *testing.T) {
	script := filepath.Join("..", "..", "packaging", "ci", "test_project_distributed.py")
	if _, e := os.Stat(script); os.IsNotExist(e) {
		t.Skip("CI configs outside module snapshot")
	}
	out, e := exec.Command("python3", "-B", script).CombinedOutput()
	if e != nil {
		t.Fatalf("%v: %s", e, out)
	}
}
