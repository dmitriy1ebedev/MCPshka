package officedocs

import (
	"os/exec"
	"path/filepath"
)

func runWorker(dir string) (string, error) {
	out, err := exec.Command(python, "-I", "-B", filepath.Join(dir, "office.py"), filepath.Join(dir, "spec.json")).CombinedOutput()
	return string(out), err
}
