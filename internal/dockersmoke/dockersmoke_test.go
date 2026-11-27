package dockersmoke

import (
	"mcpshka/internal/selftest"
	"os"
	"strings"
	"testing"
	"time"
)

func dockerSmokeFixture() DockerSmokeRequest {
	return DockerSmokeRequest{Repo: "/root/repos/diag", Commit: strings.Repeat("a", 40), Dockerfile: "packaging/docker/Dockerfile",
		BuildArgs: map[string]string{"VERSION": "0.1.12-dev.1"},
		Runs: []DockerSmokeRun{
			{Name: "scope", Profile: "isolated", Args: []string{"scope", "--json"}},
			{Name: "persist", Profile: "isolated", Entrypoint: "/bin/sh", Args: []string{"-c", "diagd --run-for 2s && cat /tmp/x"}, Volume: "/var/lib/network-diag"},
			{Name: "host", Profile: "host-observer", Env: map[string]string{"DIAG_HOST_OBSERVER": "1"}, Args: []string{"capabilities", "--json"}},
		}}
}

func TestDockerSmokeValidation(t *testing.T) {
	if err := validateDockerSmoke(dockerSmokeFixture()); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*DockerSmokeRequest){
		"repo outside":    func(r *DockerSmokeRequest) { r.Repo = "/etc" },
		"repo traversal":  func(r *DockerSmokeRequest) { r.Repo = "/root/repos/.." },
		"short commit":    func(r *DockerSmokeRequest) { r.Commit = "abc" },
		"dockerfile up":   func(r *DockerSmokeRequest) { r.Dockerfile = "../x/Dockerfile" },
		"dockerfile abs":  func(r *DockerSmokeRequest) { r.Dockerfile = "/Dockerfile" },
		"build arg value": func(r *DockerSmokeRequest) { r.BuildArgs["VERSION"] = "$(id)" },
		"no runs":         func(r *DockerSmokeRequest) { r.Runs = nil },
		"privileged":      func(r *DockerSmokeRequest) { r.Runs[0].Profile = "privileged" },
		"entrypoint":      func(r *DockerSmokeRequest) { r.Runs[0].Entrypoint = "/usr/bin/nsenter" },
		"duplicate name":  func(r *DockerSmokeRequest) { r.Runs[1].Name = "scope" },
		"volume path":     func(r *DockerSmokeRequest) { r.Runs[1].Volume = "/../../etc" },
		"env newline":     func(r *DockerSmokeRequest) { r.Runs[2].Env["DIAG_HOST_OBSERVER"] = "1\nX=2" },
	}
	for name, mutate := range bad {
		req := dockerSmokeFixture()
		mutate(&req)
		if validateDockerSmoke(req) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if dockerSmokeRequestID(dockerSmokeFixture()) != dockerSmokeRequestID(dockerSmokeFixture()) {
		t.Fatal("request id is not stable")
	}
}

func TestDockerSmokeRunArgsAreHardened(t *testing.T) {
	req := dockerSmokeFixture()
	for i, run := range req.Runs {
		args := strings.Join(dockerSmokeRunArgs("id", i, "img", "vol", run), " ")
		for _, want := range []string{"--user 65532:65532", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "--pids-limit 64", "--memory 96m", "--pull never"} {
			if !strings.Contains(args, want) {
				t.Errorf("run %s lacks %q: %s", run.Name, want, args)
			}
		}
		for _, banned := range []string{"--privileged", "docker.sock", "--cap-add", "--device"} {
			if strings.Contains(args, banned) {
				t.Errorf("run %s has %q", run.Name, banned)
			}
		}
		if !strings.HasSuffix(args, "img "+strings.Join(run.Args, " ")) {
			t.Errorf("image and args must come last: %s", args)
		}
	}
	isolated := strings.Join(dockerSmokeRunArgs("id", 0, "img", "vol", req.Runs[0]), " ")
	if !strings.Contains(isolated, "--network none") || strings.Contains(isolated, "--pid host") {
		t.Fatalf("isolated profile shares host namespaces: %s", isolated)
	}
	host := strings.Join(dockerSmokeRunArgs("id", 2, "img", "vol", req.Runs[2]), " ")
	if !strings.Contains(host, "--pid host") || !strings.Contains(host, "src=/sys,dst=/host/sys,readonly") || !strings.Contains(host, "-e DIAG_HOST_OBSERVER=1") {
		t.Fatalf("host-observer profile incomplete: %s", host)
	}
	if !strings.Contains(strings.Join(dockerSmokeRunArgs("id", 1, "img", "vol", req.Runs[1]), " "), "type=volume,src=vol,dst=/var/lib/network-diag") {
		t.Fatal("volume not mounted")
	}
}

// BUG-20261024-106: the broker unit can write only under the executor
// authority root; /var/lib/mcpshka-docker-smoke was read-only to it.
func TestDockerSmokeStateIsUnderAuthorityRoot(t *testing.T) {
	if !strings.HasPrefix(dockerSmokeRoot, "/var/lib/mcpshka-executor/") {
		t.Fatalf("docker smoke state %s is outside the broker's ReadWritePaths", dockerSmokeRoot)
	}
}

func TestDockerSmokeAliveTransitionalStates(t *testing.T) {
	now := time.Now()
	running := DockerSmokeResult{State: "RUNNING", StartedAt: now.Add(-time.Hour)}
	for _, s := range []string{"active", "activating", "reloading", "deactivating"} {
		if !dockerSmokeAlive(running, now, s) {
			t.Errorf("%s: worker reported dead", s)
		}
	}
	for _, s := range []string{"inactive", "failed", ""} {
		if dockerSmokeAlive(running, now, s) {
			t.Errorf("%s: dead worker reported alive", s)
		}
	}
	fresh := DockerSmokeResult{State: "STARTING", StartedAt: now.Add(-10 * time.Second)}
	if !dockerSmokeAlive(fresh, now, "inactive") {
		t.Error("fresh STARTING before systemd loads the unit reported dead")
	}
	stale := DockerSmokeResult{State: "STARTING", StartedAt: now.Add(-StartGrace - time.Second)}
	if dockerSmokeAlive(stale, now, "inactive") {
		t.Error("STARTING past the grace window reported alive")
	}
}

func TestSelfTestState(t *testing.T) {
	req := map[string]bool{"a": true}
	if s := selftest.State([]selftest.Check{{Name: "a", OK: true}, {Name: "b", OK: true}}, req); s != "ready" {
		t.Fatal(s)
	}
	if s := selftest.State([]selftest.Check{{Name: "a", OK: true}, {Name: "b"}}, req); s != "degraded" {
		t.Fatal(s)
	}
	if s := selftest.State([]selftest.Check{{Name: "a"}, {Name: "b", OK: true}}, req); s != "blocked" {
		t.Fatal(s)
	}
}

func TestProbeWritableDirLeavesNothing(t *testing.T) {
	dir := t.TempDir() + "/state"
	if err := selftest.ProbeWritableDir(dir); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("probe left %d entries", len(left))
	}
}
