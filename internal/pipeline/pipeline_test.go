package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/evidence"
	"mcpshka/internal/executor"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/statestore"
	"mcpshka/internal/workbenchruntime"
	"time"
)

type pipelineTestRuntime struct {
	starts int
	code   int
}

func (*pipelineTestRuntime) Name() string { return workbenchruntime.New(nil).Name() }
func (*pipelineTestRuntime) Match(command string, args []string, root string) bool {
	return command == workbenchruntime.StageCommand
}
func (*pipelineTestRuntime) Plan(id, command string, args []string, root string, opts jobs.StartOptions) (json.RawMessage, error) {
	return json.RawMessage(`{"fixture":true}`), nil
}
func (r *pipelineTestRuntime) Start(json.RawMessage) error { r.starts++; return nil }
func (r *pipelineTestRuntime) Observe(json.RawMessage, bool) (jobs.ExternalObservation, error) {
	state := jobs.Done
	if r.code != 0 {
		state = jobs.Failed
	}
	return jobs.ExternalObservation{State: state, ExitCode: &r.code, ResourcesReleased: true}, nil
}

func pipelineFixture(t *testing.T) (Dependencies, *pipelineTestRuntime, string) {
	t.Helper()
	oldDir := evidence.Dir
	evidence.Dir = t.TempDir()
	t.Cleanup(func() { evidence.Dir = oldDir })
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if raw, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", raw, e)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.invalid")
	git("config", "user.name", "test")
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module pipelinefixture\n\ngo 1.26\n"), 0600)
	git("add", "go.mod")
	git("commit", "-qm", "fixture")
	p := policy.Default()
	p.AllowedWorkdirs = []string{root}
	p.DefaultProfile = policy.ProfileOperator
	state := t.TempDir()
	db, err := statestore.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	projectprofile.SetRegistry(db)
	t.Cleanup(func() { projectprofile.SetRegistry(nil) })
	profiles := projectprofile.New(p)
	workflows := map[string]projectprofile.Workflow{}
	for _, stage := range projectprofile.StageOrder {
		workflows[stage] = projectprofile.Workflow{Command: "make", Args: []string{stage}, TimeoutSeconds: 60, OutputLimitBytes: 4096}
	}
	w := workflows["package"]
	w.Artifacts = []string{"app.ipk"}
	workflows["package"] = w
	_, err = profiles.Register(root, projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "fixture", Workflows: workflows})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(0) })
	runtime := &pipelineTestRuntime{}
	manager.SetExternalRuntime(runtime)
	return Dependencies{Manager: manager, Git: gitops.New(p), Profiles: profiles, Options: func(p projectprofile.Plan, _, _ int64) jobs.StartOptions {
		return jobs.StartOptions{Timeout: time.Duration(p.TimeoutSeconds) * time.Second, OutputLimitBytes: p.OutputLimitBytes}
	}}, runtime, root
}

func TestPipelineProgressReuseAndInvalidation(t *testing.T) {
	d, r, root := pipelineFixture(t)
	ctx := context.Background()
	in := Input{Path: root, Stages: []string{"prepare", "build", "test", "lint", "verify"}}
	for i := 0; i < 6; i++ {
		out, err := Advance(ctx, d, in)
		if err != nil {
			t.Fatal(err)
		}
		if i < 5 && out.State != "RUNNING" {
			t.Fatalf("stage %d: %+v", i, out)
		}
		if i == 5 && (out.State != "PASS" || len(out.Evidence) != 5) {
			t.Fatalf("completion: %+v", out)
		}
	}
	if r.starts != 5 {
		t.Fatalf("starts=%d", r.starts)
	}
	out, err := Advance(ctx, d, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range out.Evidence {
		if !e.Reused {
			t.Fatal("PASS not reused")
		}
	}
	if r.starts != 5 {
		t.Fatal("reused execution restarted")
	}
	// A new commit with the identical tree still invalidates every stage.
	cmd := exec.Command("git", "-C", root, "commit", "--allow-empty", "-qm", "new HEAD")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", raw, err)
	}
	out, err = Advance(ctx, d, in)
	if err != nil || out.State != "RUNNING" || r.starts != 6 {
		t.Fatalf("HEAD not invalidated: %+v %v", out, err)
	}
	// Configuration outside Git participates in the digest too.
	resolved, err := d.Profiles.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	w := resolved.Profile.Workflows["prepare"]
	w.Env = map[string]string{"FLAG": "changed"}
	resolved.Profile.Workflows["prepare"] = w
	if _, err = d.Profiles.Register(root, resolved.Profile); err != nil {
		t.Fatal(err)
	}
	out, err = Advance(ctx, d, in)
	if err != nil || r.starts != 7 {
		t.Fatalf("config not invalidated: %+v %v", out, err)
	}
	os.WriteFile(filepath.Join(root, "dirty"), []byte("uncommitted"), 0600)
	if _, err = Advance(ctx, d, in); err == nil {
		t.Fatal("dirty inputs admitted")
	}
}

func TestPipelineFailureAndPrevalidation(t *testing.T) {
	d, r, root := pipelineFixture(t)
	ctx := context.Background()
	resolved, _ := d.Profiles.Resolve(root)
	delete(resolved.Profile.Workflows, "verify")
	if _, err := d.Profiles.Register(root, resolved.Profile); err != nil {
		t.Fatal(err)
	}
	if _, err := Advance(ctx, d, Input{Path: root}); err == nil || r.starts != 0 {
		t.Fatal("partial plan executed before validation")
	}
	r.code = 7
	in := Input{Path: root, Stages: []string{"build", "test"}}
	if _, err := Advance(ctx, d, in); err != nil {
		t.Fatal(err)
	}
	out, err := Advance(ctx, d, in)
	if err != nil || out.State != "FAIL" || r.starts != 1 {
		t.Fatalf("failure advanced: %+v %v", out, err)
	}
}

func TestPipelineManifestTamperAndIdentity(t *testing.T) {
	dir := t.TempDir()
	data := []byte("IPK")
	sum := sha256.Sum256(data)
	m := PipelineManifest{Schema: "mcpshka.pipeline/v1", Project: "/project", HEAD: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40), ProfileDigest: strings.Repeat("c", 64), Stage: "package", State: "PASS", ManifestPath: filepath.Join(dir, "manifest.json"), Artifacts: []executor.PipelineArtifact{{Path: "app.ipk", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}}}
	raw, _ := json.Marshal(m)
	os.WriteFile(m.ManifestPath, raw, 0600)
	os.WriteFile(filepath.Join(dir, "app.ipk"), data, 0600)
	if err := verifyPipelineManifest(m); err != nil {
		t.Fatal(err)
	}
	id := pipelineIdentity(m)
	for _, mutate := range []func(*PipelineManifest){func(m *PipelineManifest) { m.HEAD = "different" }, func(m *PipelineManifest) { m.Tree = "different" }, func(m *PipelineManifest) { m.ProfileDigest = "different" }} {
		copy := m
		mutate(&copy)
		if pipelineIdentity(copy) == id {
			t.Fatal("identity reused changed input")
		}
		if verifyPipelineManifest(copy) == nil {
			t.Fatal("wrong manifest accepted")
		}
	}
	os.WriteFile(filepath.Join(dir, "app.ipk"), []byte("bad"), 0600)
	if verifyPipelineManifest(m) == nil {
		t.Fatal("corrupt artifact reused")
	}
}

func TestPipelineRefusesRevokedSharedProof(t *testing.T) {
	d, _, root := pipelineFixture(t)
	in := Input{Path: root, Stages: []string{"build"}}
	for i := 0; i < 2; i++ {
		if _, err := Advance(context.Background(), d, in); err != nil {
			t.Fatal(err)
		}
	}
	r, ok := evidence.Get(root, "build", "pipeline/build")
	if !ok {
		t.Fatal("missing journal proof")
	}
	r.Result = "FAIL"
	if err := evidence.Put(r); err != nil {
		t.Fatal(err)
	}
	if _, err := Advance(context.Background(), d, in); err == nil {
		t.Fatal("reused manifest despite revoked shared proof")
	}
}

// MCP-202: a reported FAIL (cancelled package on a clean HEAD) is not a
// verdict forever; the next call starts a fresh, numbered attempt.
func TestPipelineRetriesReportedFailure(t *testing.T) {
	d, r, root := pipelineFixture(t)
	ctx := context.Background()
	r.code = 130
	in := Input{Path: root, Stages: []string{"build"}}
	if _, err := Advance(ctx, d, in); err != nil {
		t.Fatal(err)
	}
	if out, err := Advance(ctx, d, in); err != nil || out.State != "FAIL" {
		t.Fatalf("failure not reported: %+v %v", out, err)
	}
	// The fixture runtime observes the current code for every job, so the
	// retry keeps failing; what matters is a new job and a numbered attempt.
	out, err := Advance(ctx, d, in)
	if err != nil || out.State != "RUNNING" || r.starts != 2 || out.Evidence[0].Attempt != 2 {
		t.Fatalf("retry replayed the failed job: %+v %v starts=%d", out, err, r.starts)
	}
	if out, err = Advance(ctx, d, in); err != nil || out.State != "FAIL" || out.Evidence[0].Attempt != 2 {
		t.Fatalf("second attempt not reported: %+v %v", out, err)
	}
}
