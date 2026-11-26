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

// A Lane-shaped recipe: stage, wait for a gate, install, verify; a foreign
// project would differ only in this file.
const recipe = `{
 "schema": "mcpshka.release/v1",
 "version_file": "VERSION",
 "env": {"LANE_HOME": "/root/repos/lane"},
 "steps": [
  {"name": "stage", "root": true, "argv": ["./lane", "release", "stage"], "capture": {"release_id": "release-[0-9a-f]{4}"}},
  {"name": "gate", "root": true, "argv": ["./lane", "release", "gate-status", "--release-id", "{release_id}"], "retry_seconds": 5, "expect": "SUCCESS", "fail": "ERROR", "timeout_seconds": 600},
  {"name": "install", "root": true, "argv": ["./lane", "upgrade", "start", "--release-id", "{release_id}"]},
  {"name": "verify", "argv": ["/usr/bin/true", "{version}"], "expect": "HEALTHY"}
 ],
 "rollback": {"name": "back", "root": true, "argv": ["./lane", "upgrade", "rollback"]},
 "rollback_from": "install"
}`

func fakeWorker(t *testing.T, pinned bool, run func(s Step, argv []string) ([]byte, error)) *worker {
	t.Helper()
	StateDir = t.TempDir()
	commit := strings.Repeat("a", 40)
	if pinned {
		if err := os.MkdirAll(filepath.Dir(pinPath("lane")), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pinPath("lane"), []byte(Digest([]byte(recipe))+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &worker{
		root: "/root/repos/lane",
		res:  Result{Project: "lane", Commit: commit, Version: "1.6.0", State: "RUNNING"},
		git: func(_ context.Context, a ...string) ([]byte, error) {
			switch {
			case a[0] == "rev-parse":
				return []byte(commit + "\n"), nil
			case a[0] == "show" && strings.HasSuffix(a[1], RecipePath):
				return []byte(recipe), nil
			case a[0] == "show":
				return []byte("1.6.0\n"), nil
			}
			return nil, nil
		},
		run:   func(_ context.Context, s Step, argv, _ []string) ([]byte, error) { return run(s, argv) },
		sleep: func(time.Duration) {},
		save:  func(Result) {},
		ci:    func(string) *projectprofile.CI { return nil },
	}
}

func TestReleaseRunsRecipeWithCapturesAndWaits(t *testing.T) {
	var calls []string
	gate := 0
	w := fakeWorker(t, true, func(s Step, argv []string) ([]byte, error) {
		calls = append(calls, strings.Join(argv, " "))
		switch s.Name {
		case "stage":
			return []byte(`{"release_id": "release-beef"}`), nil
		case "gate":
			if gate++; gate < 3 {
				return []byte("RUNNING"), nil
			}
			return []byte("SUCCESS"), nil
		case "verify":
			return []byte("HEALTHY"), nil
		}
		return nil, nil
	})
	w.release(context.Background())
	if w.res.State != "INSTALLED" || len(w.res.Steps) != 4 || w.res.Steps[1].Tries != 3 {
		t.Fatalf("%+v", w.res)
	}
	if calls[1] != "/root/repos/lane/lane release gate-status --release-id release-beef" || calls[len(calls)-1] != "/usr/bin/true 1.6.0" {
		t.Fatalf("argv: %q", calls)
	}
}

func TestUnapprovedRecipeRunsNothing(t *testing.T) {
	ran := 0
	w := fakeWorker(t, false, func(Step, []string) ([]byte, error) { ran++; return nil, nil })
	w.release(context.Background())
	if w.res.State != "RECIPE_NOT_APPROVED" || ran != 0 || !strings.Contains(w.res.Next, "project-release-pin") || !strings.Contains(w.res.Next, Digest([]byte(recipe))) {
		t.Fatalf("%+v ran %d", w.res, ran)
	}
}

func TestFailureRollsBackOnlyAfterInstallStarted(t *testing.T) {
	for _, tc := range []struct{ failing, state string }{{"gate", "FAILED"}, {"verify", "ROLLED_BACK"}} {
		back := 0
		w := fakeWorker(t, true, func(s Step, _ []string) ([]byte, error) {
			switch {
			case s.Name == "back":
				back++
			case s.Name == tc.failing && s.Name == "gate":
				return []byte("ERROR"), nil
			case s.Name == tc.failing:
				return []byte("DEGRADED"), nil
			case s.Name == "stage":
				return []byte("release-beef"), nil
			case s.Name == "gate":
				return []byte("SUCCESS"), nil
			}
			return nil, nil
		})
		w.release(context.Background())
		if w.res.State != tc.state || (back == 1) != (tc.state == "ROLLED_BACK") || !strings.Contains(w.res.Error, tc.failing) {
			t.Fatalf("%s: %+v back %d", tc.failing, w.res, back)
		}
	}
	w := fakeWorker(t, true, func(s Step, _ []string) ([]byte, error) {
		if s.Name == "stage" {
			return []byte("release-beef"), nil
		}
		if s.Name == "gate" {
			return []byte("SUCCESS"), nil
		}
		return nil, errors.New("exit status 1")
	})
	w.release(context.Background())
	if w.res.State != "ROLLBACK_FAILED" {
		t.Fatalf("%+v", w.res)
	}
}

func TestWrongCheckoutOrVersionIsRefused(t *testing.T) {
	w := fakeWorker(t, true, func(Step, []string) ([]byte, error) { return nil, nil })
	w.res.Version = "1.7.0"
	if w.release(context.Background()); w.res.State != "REFUSED" || !strings.Contains(w.res.Error, "VERSION") {
		t.Fatalf("%+v", w.res)
	}
	w = fakeWorker(t, true, func(Step, []string) ([]byte, error) { return nil, nil })
	w.res.Commit = strings.Repeat("b", 40)
	if w.release(context.Background()); w.res.State != "REFUSED" || !strings.Contains(w.res.Error, "checkout") {
		t.Fatalf("%+v", w.res)
	}
}

func TestRecipeIsStrict(t *testing.T) {
	if _, err := ParseRecipe([]byte(recipe)); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"unknown field":    strings.Replace(recipe, `"version_file"`, `"shell": "rm -rf /", "version_file"`, 1),
		"shell argv0":      strings.Replace(recipe, `"./lane", "release", "stage"`, `"sh", "-c", "x"`, 1),
		"unknown variable": strings.Replace(recipe, "{version}", "{password}", 1),
		"PATH override":    strings.Replace(recipe, `"LANE_HOME"`, `"PATH"`, 1),
		"retry no expect":  strings.Replace(recipe, `"expect": "SUCCESS", `, "", 1),
		"rollback_from":    strings.Replace(recipe, `"rollback_from": "install"`, `"rollback_from": "nope"`, 1),
	} {
		if _, err := ParseRecipe([]byte(bad)); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	in := Input{Path: "/root/repos/mcpshka", Commit: strings.Repeat("a", 40), Version: "1.0.0"}
	if _, err := in.BrokerArgs("o"); err == nil {
		t.Error("MCPshka itself is not released this way")
	}
	in.Path = "/root/repos/lane/../diag"
	if _, err := in.BrokerArgs("o"); err == nil {
		t.Error("unclean path")
	}
	in.Path = "/root/repos/kopilka"
	if args, err := in.BrokerArgs("o"); err != nil || args[3] != "kopilka" {
		t.Errorf("any project: %v %v", args, err)
	}
}

func TestReleaseUnitStatePropertyOrder(t *testing.T) {
	for _, raw := range []string{"Result=success\nActiveState=activating\n", "ActiveState=activating\nResult=success\n"} {
		out := reconcileUnit(Result{State: "RUNNING"}, []byte(raw))
		if out.State != "RUNNING" {
			t.Fatal(out)
		}
	}
	stale := reconcileUnit(Result{State: "FAILED", Error: "previous attempt", FinishedAt: "old"}, []byte("Result=success\nActiveState=activating"))
	if stale.State != "RUNNING" || stale.Error != "" {
		t.Fatal(stale)
	}
	out := reconcileUnit(Result{State: "RUNNING"}, []byte("ActiveState=failed\nResult=timeout"))
	if out.State != "FAILED" {
		t.Fatal(out)
	}
	out = reconcileUnit(Result{State: "RUNNING"}, nil)
	if out.State != "RUNNING" {
		t.Fatal("unknown is not failure", out)
	}
}
