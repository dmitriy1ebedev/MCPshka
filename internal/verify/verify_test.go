package verify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/policy"
)

func writeVersionBinary(t *testing.T, path, version string) {
	t.Helper()
	body := "#!/bin/sh\nprintf 'mcpshka " + version + "\\n'\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = policy.MinimalEnv(t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func testVerifier(t *testing.T) (*Service, string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "mcpshka")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.test/mcpshka\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", repo)
	cmd.Env = policy.MinimalEnv(base)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	gitRun(t, repo, "add", "go.mod")
	gitRun(t, repo, "-c", "user.name=MCPshka Test", "-c", "user.email=test@localhost", "commit", "-qm", "init")

	current := filepath.Join(base, "mcpshka-current")
	previous := filepath.Join(base, "mcpshka-previous")
	writeVersionBinary(t, current, "0.1.a.25")
	writeVersionBinary(t, previous, "0.1.a.24")

	p := &policy.Policy{AllowedWorkdirs: []string{base}, DeniedCommands: map[string]struct{}{}}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "list-units" {
			return []byte("docker.service loaded active running\ncontainerd.service loaded active running\n"), nil
		}
		if name == "systemctl" && len(args) == 3 && args[2] == "mcpshka-heavy.slice" {
			return []byte("CPUWeight=20\nIOWeight=10\nCPUQuotaPerSecUSec=3s\n"), nil
		}
		if name == "systemctl" && len(args) == 3 && args[0] == "show" && args[2] == "mcpshka-broker.service" {
			return []byte("CPUQuotaPerSecUSec=1s\nCPUWeight=400\nIOWeight=400\nTasksMax=96\n"), nil
		}
		if name == "systemctl" && len(args) == 3 && args[0] == "show" {
			if args[2] == "user.slice" {
				return []byte("CPUWeight=30\nIOWeight=30\nCPUQuotaPerSecUSec=3s\n"), nil
			}
			return []byte("CPUWeight=1000\nIOWeight=1000\nCPUQuotaPerSecUSec=infinity\n"), nil
		}
		if name != "systemctl" || len(args) != 2 || args[0] != "is-active" {
			return nil, fmt.Errorf("unexpected command %s %v", name, args)
		}
		switch args[1] {
		case "mcpshka.service", "mcpshka-tunnel.service":
			return []byte("active\n"), nil
		default:
			return []byte("inactive\n"), fmt.Errorf("inactive")
		}
	}
	get := func(_ context.Context, url string) (int, string, error) {
		switch url {
		case "http://127.0.0.1:8765/healthz":
			return 200, `{"name":"MCPshka","status":"ok","version":"0.1.a.25"}`, nil
		case "http://127.0.0.1:8768/readyz":
			return 200, "ready", nil
		case "https://api.openai.com/":
			return 421, "", nil
		default:
			return 0, "", fmt.Errorf("unexpected url %q", url)
		}
	}
	lookup := func(_ context.Context, host string) ([]string, error) {
		if host != "api.openai.com" {
			return nil, fmt.Errorf("unexpected host %q", host)
		}
		return []string{"172.66.0.243", "162.159.140.245"}, nil
	}
	return newService(p, repo, current, previous, run, get, lookup), repo
}

func findCheck(t *testing.T, result Result, name string) Check {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing check %q: %+v", name, result.Checks)
	return Check{}
}

func TestVerifyMCPshkaProductionHealthy(t *testing.T) {
	s, repo := testVerifier(t)
	result, err := s.Verify(context.Background(), repo, "0.1.a.25")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || !result.ExternalConnectivityOK || !result.GitClean || result.Profile != "mcpshka-native" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.InstalledVersion != "0.1.a.25" || result.RollbackVersion != "0.1.a.24" {
		t.Fatalf("versions: %+v", result)
	}
	if len(result.Checks) != 11 {
		t.Fatalf("checks=%d want=11: %+v", len(result.Checks), result.Checks)
	}
	for _, check := range result.Checks {
		if !check.OK {
			t.Fatalf("failed check: %+v", check)
		}
	}
}

func TestVerifyRejectsBrokerResourceDrift(t *testing.T) {
	s, repo := testVerifier(t)
	original := s.run
	s.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) == 3 && args[0] == "show" {
			return []byte("CPUQuotaPerSecUSec=500ms\nCPUWeight=400\nIOWeight=400\nTasksMax=32\n"), nil
		}
		return original(ctx, name, args...)
	}
	result, err := s.Verify(context.Background(), repo, "0.1.a.25")
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || findCheck(t, result, "broker_resources").OK {
		t.Fatal("broker resource drift accepted", result)
	}
}

func TestVerifyDetectsDirtyGit(t *testing.T) {
	s, repo := testVerifier(t)
	// Untracked local documents (ROADMAP, registries) are ignored, as
	// publication ignores them; verify used to fail on them (BUG-20261103-6).
	if err := os.WriteFile(filepath.Join(repo, "ROADMAP.md"), []byte("local"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := s.Verify(context.Background(), repo, "0.1.a.25")
	if err != nil {
		t.Fatal(err)
	}
	if check := findCheck(t, result, "git_clean"); !check.OK || !result.GitClean || !strings.Contains(check.Detail, "1 untracked") {
		t.Fatalf("untracked local file failed verify: %+v", check)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = s.Verify(context.Background(), repo, "0.1.a.25")
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.GitClean {
		t.Fatalf("tracked change reported healthy: %+v", result)
	}
	check := findCheck(t, result, "git_clean")
	if check.OK || !strings.Contains(check.Detail, "tracked changes") {
		t.Fatalf("unexpected git check: %+v", check)
	}
}

func TestVerifyDetectsVersionMismatch(t *testing.T) {
	s, repo := testVerifier(t)
	result, err := s.Verify(context.Background(), repo, "0.1.a.26")
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatalf("version mismatch reported healthy: %+v", result)
	}
	check := findCheck(t, result, "installed_version")
	if check.OK || check.Detail != "0.1.a.25" {
		t.Fatalf("unexpected version check: %+v", check)
	}
}

func TestVerifyRejectsUnsupportedProjectAndBadVersion(t *testing.T) {
	s, repo := testVerifier(t)
	other := filepath.Join(filepath.Dir(repo), "other")
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(context.Background(), other, "0.1.a.25"); err == nil {
		t.Fatal("accepted unsupported project")
	}
	for _, version := range []string{"", "../x", "0.1.a.25 bad"} {
		if _, err := s.Verify(context.Background(), repo, version); err == nil {
			t.Fatalf("accepted invalid expected version %q", version)
		}
	}
}

func TestVerifyExternalFailureKeepsLocalHealthButTunnelFailureDoesNot(t *testing.T) {
	for _, tunnelReady := range []bool{true, false} {
		t.Run(fmt.Sprint(tunnelReady), func(t *testing.T) {
			s, repo := testVerifier(t)
			get := s.get
			s.get = func(ctx context.Context, url string) (int, string, error) {
				if url == "https://api.openai.com/" {
					return 0, "", fmt.Errorf("external HTTPS unavailable")
				}
				if url == "http://127.0.0.1:8768/readyz" && !tunnelReady {
					return 503, "not ready", nil
				}
				return get(ctx, url)
			}
			s.lookup = func(context.Context, string) ([]string, error) { return nil, fmt.Errorf("external DNS unavailable") }
			r, err := s.Verify(context.Background(), repo, "0.1.a.25")
			if err != nil || r.OK != tunnelReady {
				t.Fatalf("local health=%v tunnel ready=%v err=%v", r.OK, tunnelReady, err)
			}
			if r.ExternalConnectivityOK || findCheck(t, r, "dns").OK || findCheck(t, r, "outbound_https").OK {
				t.Fatal("external failures hidden")
			}
		})
	}
}
