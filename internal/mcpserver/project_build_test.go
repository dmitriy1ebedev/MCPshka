package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
)

func TestProjectBuildToolStartsAndCompletesBoundedGoBuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("build:\n\t@printf ok\n"), 0600); err != nil {
		t.Fatal(err)
	}

	p := &policy.Policy{
		AllowedWorkdirs: []string{root},
		DeniedCommands:  map[string]struct{}{},
		// A temporary Makefile is executed only inside this isolated test fixture.
		// Production policy denies unsandboxed recipe execution by default.
		AllowedCommandClasses: map[policy.CommandClass]bool{policy.ClassReadOnly: true, policy.ClassMutating: true, policy.ClassPrivileged: true},
	}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	server := New(manager, filesystem.New(p))
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "project_build",
		Arguments: map[string]any{
			"idempotency_key":    "bounded-build",
			"path":               root,
			"timeout_seconds":    30,
			"output_limit_bytes": 1024 * 1024,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("project_build returned tool error: %+v", res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out ProjectBuildResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Plan.Command != "make" || len(out.Plan.Args) != 1 || out.Plan.Args[0] != "build" {
		t.Fatalf("unexpected build plan: %+v", out.Plan)
	}
	if out.Job.ID == "" {
		t.Fatal("project_build did not return a job id")
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s, err := manager.Get(out.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch s.State {
		case jobs.Done:
			return
		case jobs.Failed, jobs.Cancelled:
			stdout, _ := manager.Read(out.Job.ID, "stdout", 0, 16*1024)
			stderr, _ := manager.Read(out.Job.ID, "stderr", 0, 16*1024)
			t.Fatalf("build ended %s: err=%q stdout=%q stderr=%q", s.State, s.Error, stdout.Text, stderr.Text)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("project_build job did not finish before test deadline")
}

func TestProjectTestGoUsesManagedTemporaryStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root, state := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module fixture.test\n\ngo 1.26\n",
		"environment_test.go": `package fixture
import ("os"; "path/filepath"; "testing")
func TestEnvironment(t *testing.T) {
 if err := os.WriteFile(filepath.Join(os.TempDir(), "environment"), []byte(os.Getenv("GOMAXPROCS")+"\n"+os.TempDir()), 0600); err != nil { t.Fatal(err) }
}
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}, AllowedCommandClasses: map[policy.CommandClass]bool{policy.ClassReadOnly: true, policy.ClassMutating: true}}
	manager, err := jobs.New(state, p)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(time.Second)
	// Reuse the suite's standard-library cache rather than rebuild it in a fixture.
	cache, err := exec.CommandContext(ctx, "go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(state, ".home", ".cache")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(strings.TrimSpace(string(cache)), filepath.Join(cacheDir, "go-build")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".mcpshka"), 0700); err != nil {
		t.Fatal(err)
	}
	profile := `{"schema":"mcpshka.project-profile/v1","name":"external-fixture","match":{"module":"fixture.test","markers":["go.mod"]},"ci":{"provider":"distributed","external_only":true},"workflows":{"test":{"command":"go","args":["test","./..."],"timeout_seconds":80,"output_limit_bytes":1048576}}}`
	if err := os.WriteFile(filepath.Join(root, ".mcpshka/profile.json"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectprofile.New(p).Resolve(root); err != nil {
		t.Fatal("fixture profile:", err)
	}
	if ci := projectprofile.New(p).CIFor(root); ci == nil || !ci.ExternalOnly {
		t.Fatal("external-only fixture profile was not resolved")
	}
	client := uploadClient(t, manager, "test-runner", "operator")
	res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "project_test", Arguments: map[string]any{"path": root, "timeout_seconds": 80, "idempotency_key": "go-test-storage", "local": true, "packages": []string{"./..."}, "run": "^TestEnvironment$"}})
	if err != nil || res.IsError {
		t.Fatalf("project_test: %+v %v", res, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out ProjectTestResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.Plan.Args, " ") != "test -timeout=0 -p 1 -run ^TestEnvironment$ ./..." {
		t.Fatalf("filters lost: %+v", out.Plan)
	}
	conflict, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "project_test", Arguments: map[string]any{"path": root, "timeout_seconds": 80, "idempotency_key": "go-test-storage", "local": true, "packages": []string{"./..."}, "run": "Other"}})
	if err != nil || !conflict.IsError {
		t.Fatalf("changed filters replayed: %+v %v", conflict, err)
	}
	for {
		s, err := manager.Get(out.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == jobs.Done {
			break
		}
		if (s.State != jobs.Running && s.State != jobs.Queued) || ctx.Err() != nil {
			stdout, _ := manager.Read(out.Job.ID, "stdout", 0, 4096)
			stderr, _ := manager.Read(out.Job.ID, "stderr", 0, 4096)
			t.Fatalf("Go fixture did not succeed: %+v stdout=%q stderr=%q", s, stdout.Text, stderr.Text)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if out.Job.MemoryLimitBytes != goTestMemoryLimit || out.Job.CPUTimeLimitSeconds != int64(goTestCPUTimeLimit/time.Second) {
		t.Fatalf("Go test limits: memory=%d cpu=%d", out.Job.MemoryLimitBytes, out.Job.CPUTimeLimitSeconds)
	}
	tmp := filepath.Join(state, out.Job.ID, "tmp")
	got, err := os.ReadFile(filepath.Join(tmp, "environment"))
	if err != nil || string(got) != "2\n"+tmp {
		t.Fatalf("unmanaged Go environment: %q %v", got, err)
	}
}

func TestProjectTestRejectsUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(time.Second)
	client := uploadClient(t, manager, "test-targets", "operator")
	for _, pkg := range []string{"-exec=sh", "../outside", "./x;id"} {
		res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "project_test", Arguments: map[string]any{"path": root, "packages": []string{pkg}, "idempotency_key": "invalid-" + pkg}})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("accepted unsafe target %q", pkg)
		}
	}
}

func TestProjectBuildPrefersProfileOverMakefile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	profile := `{"schema":"mcpshka.project-profile/v1","name":"fixture","match":{"markers":["Makefile"]},"workflows":{"build":{"command":"go","args":["version"],"timeout_seconds":30,"output_limit_bytes":65536}}}`
	for name, body := range map[string]string{"Makefile": "build:\n\t@printf make\n", ".mcpshka/profile.json": profile} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}, AllowedCommandClasses: map[policy.CommandClass]bool{policy.ClassReadOnly: true, policy.ClassMutating: true}}
	manager, err := jobs.New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(time.Second)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(manager, filesystem.New(p)).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "project_build", Arguments: map[string]any{"idempotency_key": "profile-build", "path": root, "timeout_seconds": 30}})
	if err != nil || res.IsError {
		t.Fatalf("project_build: %+v %v", res, err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var out ProjectBuildResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Job.MemoryLimitBytes != 1536*1024*1024 {
		t.Fatalf("Go build budget cannot link MCPshka on H96: %d", out.Job.MemoryLimitBytes)
	}
	if out.Plan.Kind != "profile" || out.Plan.Command != "go" || strings.Join(out.Plan.Args, " ") != "version" {
		t.Fatalf("profile build workflow was not used: %+v", out.Plan)
	}
	// The job creates its private HOME asynchronously; finish it before TempDir cleanup.
	for {
		result, err := waitJob(ctx, manager, out.Job.ID, 30)
		if err != nil {
			t.Fatal(err)
		}
		state, _ := result.Status["state"].(string)
		if state == string(jobs.Done) {
			break
		}
		if state == string(jobs.Failed) || state == string(jobs.Cancelled) {
			t.Fatalf("profile build failed: %+v", result)
		}
	}
}
