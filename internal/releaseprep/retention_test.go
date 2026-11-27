package releaseprep

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Exercise the installed Python gate's retention decisions through the package check.
func TestNativeGateRetention(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for _, suite := range []struct{ file, code, filter string }{
		{"test_release_gate.py", "release-gate.py", "GateRetentionTests"},
		{"test_coordinated_upgrade.py", "coordinated-upgrade.py", "RetentionIntegrationTests"},
	} {
		t.Run(suite.filter, func(t *testing.T) {
			// Register subprocess inputs with Go's test cache.
			for _, file := range []string{suite.file, suite.code} {
				if _, err := os.ReadFile("../../packaging/native/" + file); err != nil {
					t.Fatal(err)
				}
			}
			out, err := exec.Command(python, "-B", "-m", "unittest", "discover", "-s", "../../packaging/native", "-p", suite.file, "-k", suite.filter).CombinedOutput()
			if err != nil {
				t.Fatalf("native retention: %v\n%s", err, out)
			}
			if strings.Contains(string(out), "Ran 0 tests") {
				t.Fatalf("native tests not discovered: %s", out)
			}
		})
	}
}

func TestNativeSelfDeploySyntax(t *testing.T) {
	if _, err := os.ReadFile("../../packaging/native/self-deploy.sh"); err != nil {
		t.Fatal(err)
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(shell, "-n", "../../packaging/native/self-deploy.sh").CombinedOutput(); err != nil {
		t.Fatalf("self-deploy syntax: %v\n%s", err, out)
	}
}
