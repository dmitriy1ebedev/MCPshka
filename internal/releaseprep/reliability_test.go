package releaseprep

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Run the real bounded helper fixtures through project_test's shared queue.
func TestNativeReliabilityMatrix(t *testing.T) {
	for _, suite := range []struct{ test, source string }{
		{"test_coordinated_upgrade.py", "coordinated-upgrade.py"},
		{"test_codex_session_client.py", "../../internal/rootbroker/codex_session_client.py"},
	} {
		t.Run(suite.test, func(t *testing.T) {
			for _, file := range []string{suite.test, suite.source} {
				if _, err := os.ReadFile("../../packaging/native/" + file); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("python3", "-B", "-m", "unittest", "discover", "-s", "../../packaging/native", "-p", suite.test)
			out, err := cmd.CombinedOutput()
			if err != nil || strings.Contains(string(out), "Ran 0 tests") {
				t.Fatalf("native reliability fixture: %v\n%s", err, out)
			}
			t.Log(strings.TrimSpace(string(out)))
		})
	}
}
