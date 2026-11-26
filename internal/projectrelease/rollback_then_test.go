package projectrelease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/projectprofile"
)

// DIAG-shaped: two binaries restored by one cp, then the service restarted.
const recipeThen = `{
 "schema": "mcpshka.release/v1",
 "steps": [
  {"name": "install", "root": true, "argv": ["/usr/bin/install", "-t", "/usr/local/bin", "build/diag"]},
  {"name": "active", "argv": ["/usr/bin/systemctl", "is-active", "diagd.service"], "expect": "^active"}
 ],
 "rollback": {"name": "restore", "root": true, "argv": ["/usr/bin/cp", "-f", "-t", "/usr/local/bin", "/usr/local/lib/diag-previous/diag"]},
 "rollback_from": "install",
 "rollback_then": [{"name": "restart", "root": true, "argv": ["/usr/bin/systemctl", "restart", "diagd.service"]}]
}`

func TestRollbackThenParse(t *testing.T) {
	r, err := ParseRecipe([]byte(recipeThen))
	if err != nil || len(r.RollbackThen) != 1 {
		t.Fatalf("rollback_then: %v %+v", err, r.RollbackThen)
	}
	bad := strings.Replace(recipeThen, `"rollback": {"name": "restore", "root": true, "argv": ["/usr/bin/cp", "-f", "-t", "/usr/local/bin", "/usr/local/lib/diag-previous/diag"]},
 "rollback_from": "install",`, "", 1)
	if _, err := ParseRecipe([]byte(bad)); err == nil {
		t.Fatal("rollback_then without rollback accepted")
	}
}

func TestRollbackThenRunsAfterRollback(t *testing.T) {
	for _, tc := range []struct {
		restartFails bool
		state        string
	}{{false, "ROLLED_BACK"}, {true, "ROLLBACK_FAILED"}} {
		StateDir = t.TempDir()
		if err := os.MkdirAll(filepath.Dir(pinPath("diag")), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pinPath("diag"), []byte(Digest([]byte(recipeThen))+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commit := strings.Repeat("b", 40)
		var calls []string
		w := &worker{
			root: "/root/repos/diag",
			ci:   func(string) *projectprofile.CI { return nil },
			res:  Result{Project: "diag", Commit: commit, Version: "0.7.1", State: "RUNNING"},
			git: func(_ context.Context, a ...string) ([]byte, error) {
				switch {
				case a[0] == "rev-parse":
					return []byte(commit + "\n"), nil
				case a[0] == "show" && strings.HasSuffix(a[1], RecipePath):
					return []byte(recipeThen), nil
				}
				return nil, nil
			},
			run: func(_ context.Context, s Step, _, _ []string) ([]byte, error) {
				calls = append(calls, s.Name)
				switch {
				case s.Name == "active":
					return []byte("failed"), nil
				case s.Name == "restart" && tc.restartFails:
					return nil, errors.New("exit 1")
				}
				return nil, nil
			},
			sleep: func(time.Duration) {},
			save:  func(Result) {},
		}
		w.release(context.Background())
		if w.res.State != tc.state || strings.Join(calls, ",") != "install,active,restore,restart" {
			t.Fatalf("%v: state %s calls %v err %s", tc.restartFails, w.res.State, calls, w.res.Error)
		}
	}
}
