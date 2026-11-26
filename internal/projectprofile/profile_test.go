package projectprofile

import (
	"encoding/json"
	"mcpshka/internal/jobs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func writeProject(t *testing.T, root, module string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+module+"\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n\t@true\nbuild:\n\t@true\nvet:\n\t@true\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDurableWorkflowCheckpointCASAndHandoff(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "state.db")
	db, err := statestore.Open(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	first, created, err := StartCheckpoint(db, "p7-canary", hash)
	if err != nil || !created {
		t.Fatalf("start: %v %v", created, err)
	}
	same, created, err := StartCheckpoint(db, "p7-canary", hash)
	if err != nil || created || same != first {
		t.Fatalf("duplicate start: %+v %v %v", same, created, err)
	}
	if _, _, err := StartCheckpoint(db, "p7-canary", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err == nil {
		t.Fatal("accepted changed plan on same workflow identity")
	}
	if _, _, err := AdvanceCheckpoint(db, first, "inspect", "done"); err == nil {
		t.Fatal("accepted pending-to-done without running")
	}
	running, ok, err := AdvanceCheckpoint(db, first, "inspect", "running")
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, _, err := AdvanceCheckpoint(db, first, "inspect", "done"); err == nil {
		t.Fatal("accepted stale writer")
	}
	done, ok, err := AdvanceCheckpoint(db, running, "inspect", "done")
	if err != nil || !ok {
		t.Fatalf("complete: %v %v", ok, err)
	}
	// Simulate a new AI session reopening the same SQLite authority.
	reopened, err := statestore.Open(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := LoadCheckpoint(reopened, "p7-canary")
	if err != nil || restored != done {
		t.Fatalf("handoff lost checkpoint: %+v %v", restored, err)
	}
	if _, _, err := AdvanceCheckpoint(reopened, restored, "deploy", "pending"); err == nil {
		t.Fatal("skipped workflow stages")
	}
	next, ok, err := AdvanceCheckpoint(reopened, restored, "modify", "pending")
	if err != nil || !ok || next.Revision != done.Revision+1 {
		t.Fatalf("next: %+v %v %v", next, ok, err)
	}
	claimed, ok, err := AdvanceCheckpoint(db, next, "modify", "running")
	if err != nil || !ok {
		t.Fatalf("claim next: %v %v", ok, err)
	}
	unknown, ok, err := AdvanceCheckpoint(db, claimed, "modify", "unknown")
	if err != nil || !ok {
		t.Fatalf("unknown: %v %v", ok, err)
	}
	if _, _, err := ReconcileCheckpoint(db, unknown, "short", "done"); err == nil {
		t.Fatal("accepted insufficient evidence")
	}
	reconciled, ok, err := ReconcileCheckpoint(db, unknown, "verified job outcome: completed", "done")
	if err != nil || !ok || reconciled.Status != "done" || reconciled.ReconciliationEvidence == "" {
		t.Fatalf("reconcile: %+v %v %v", reconciled, ok, err)
	}
	if _, _, err := ReconcileCheckpoint(db, unknown, "verified job outcome: completed", "done"); err == nil {
		t.Fatal("accepted stale reconciliation")
	}
	if _, _, err := AdvanceCheckpoint(db, unknown, "deploy", "pending"); err == nil {
		t.Fatal("replayed unknown mutation without reconciliation")
	}
}

func TestWorkflowResumeFailClosedAndFullCanary(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	current, _, err := StartCheckpoint(db, "p7-full-canary", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResumeCheckpoint(db, current.WorkflowID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err == nil {
		t.Fatal("accepted changed plan")
	}
	running, ok, err := AdvanceCheckpoint(db, current, current.Step, "running")
	if err != nil || !ok {
		t.Fatal(err)
	}
	unknown, changed, err := ResumeCheckpoint(db, current.WorkflowID, hash)
	if err != nil || !changed || unknown.Status != "unknown" {
		t.Fatalf("interrupted step: %+v %v %v", unknown, changed, err)
	}
	if _, _, err := ResumeCheckpoint(db, current.WorkflowID, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AdvanceCheckpoint(db, running, "inspect", "done"); err == nil {
		t.Fatal("stale owner advanced interrupted step")
	}
	current, ok, err = ReconcileCheckpoint(db, unknown, "independent inspect job receipt verified", "done")
	if err != nil || !ok {
		t.Fatal(err)
	}
	steps := []string{"modify", "test", "review", "commit", "build", "deploy", "reconnect", "verify", "cleanup"}
	for _, step := range steps {
		current, ok, err = AdvanceCheckpoint(db, current, step, "pending")
		if err != nil || !ok {
			t.Fatalf("pending %s: %v", step, err)
		}
		current, ok, err = AdvanceCheckpoint(db, current, step, "running")
		if err != nil || !ok {
			t.Fatalf("running %s: %v", step, err)
		}
		current, ok, err = AdvanceCheckpoint(db, current, step, "done")
		if err != nil || !ok {
			t.Fatalf("done %s: %v", step, err)
		}
	}
	finished, err := FinishCheckpoint(db, current.WorkflowID, hash)
	if err != nil || finished != current {
		t.Fatalf("finish: %+v %v", finished, err)
	}
}

func TestWorkflowJobBindingCAS(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cp, _, err := StartCheckpoint(db, "p7-job-bind", hash)
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err := AdvanceCheckpoint(db, cp, "inspect", "running")
	if err != nil || !ok {
		t.Fatal(err)
	}
	bound, ok, err := BindCheckpointJob(db, cp, "workflow-job-1")
	if err != nil || !ok || bound.JobID != "workflow-job-1" {
		t.Fatalf("binding: %+v %v", bound, err)
	}
	if _, _, err := BindCheckpointJob(db, cp, "workflow-job-2"); err == nil {
		t.Fatal("stale job owner accepted")
	}
	done, ok, err := AdvanceCheckpoint(db, bound, "inspect", "done")
	if err != nil || !ok {
		t.Fatal(err)
	}
	next, ok, err := AdvanceCheckpoint(db, done, "modify", "pending")
	if err != nil || !ok || next.JobID != "" {
		t.Fatalf("job leaked to next stage: %+v %v", next, err)
	}
}

func TestWorkflowJobReceiptReconciliation(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	jobRoot := filepath.Join(root, "jobs")
	manager, err := jobs.New(jobRoot, &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cp, _, err := StartCheckpoint(db, "p7-job-receipt", hash)
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err := AdvanceCheckpoint(db, cp, "inspect", "running")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, _, err := ReconcileCheckpointJob(db, cp, manager); err == nil {
		t.Fatal("accepted unbound job")
	}
	job, err := manager.StartWithOptions("true", nil, root, jobs.StartOptions{Timeout: 10 * time.Second, OutputLimitBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err = BindCheckpointJob(db, cp, job.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := manager.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == jobs.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cp, ok, err = ReconcileCheckpointJob(db, cp, manager)
	if err != nil || !ok || cp.Status != "done" {
		t.Fatalf("receipt: %+v %v", cp, err)
	}
	if _, _, err := ReconcileCheckpointJob(db, cp, manager); err == nil {
		t.Fatal("replayed terminal receipt")
	}
}

func TestWorkflowFailedJobReceiptHolds(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "failed.db"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(filepath.Join(root, "jobs"), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cp, _, err := StartCheckpoint(db, "p7-failed-job", hash)
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err := AdvanceCheckpoint(db, cp, "inspect", "running")
	if err != nil || !ok {
		t.Fatal(err)
	}
	job, err := manager.StartWithOptions("false", nil, root, jobs.StartOptions{Timeout: 10 * time.Second, OutputLimitBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err = BindCheckpointJob(db, cp, job.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := manager.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == jobs.Failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	held, ok, err := ReconcileCheckpointJob(db, cp, manager)
	if err != nil || !ok || held.Status != "hold" {
		t.Fatalf("failed job not fenced: %+v %v", held, err)
	}
	restored, err := LoadCheckpoint(db, cp.WorkflowID)
	if err != nil || restored != held {
		t.Fatalf("failed receipt not durable: %+v %v", restored, err)
	}
}

func TestPinnedJobReceiptRejectsUnrelatedCommand(t *testing.T) {
	root := t.TempDir()
	db, err := statestore.Open(filepath.Join(root, "pinned.db"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(filepath.Join(root, "jobs"), &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cp, _, err := StartCheckpoint(db, "p7-pinned-receipt", hash)
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err := AdvanceCheckpoint(db, cp, "inspect", "running")
	if err != nil || !ok {
		t.Fatal(err)
	}
	job, err := manager.StartWithOptions("true", nil, root, jobs.StartOptions{Timeout: 10 * time.Second, OutputLimitBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	// The fast process may still be writing its durable receipt while the
	// test assertions complete. Wait before TempDir's cleanup removes it.
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			snapshot, getErr := manager.Get(job.ID)
			if getErr == nil && (snapshot.State == jobs.Done || snapshot.State == jobs.Failed ||
				snapshot.State == jobs.Cancelled || snapshot.State == jobs.TimedOut) {
				return
			}
			if time.Now().After(deadline) {
				_, _ = manager.Cancel(job.ID)
				t.Error("background job did not release its receipt directory")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	cp, ok, err = BindCheckpointJob(db, cp, job.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	wrong := Plan{Fingerprint: hash, Root: root, Command: "false"}
	if _, _, err := ReconcilePlannedJob(db, cp, wrong, manager); err == nil {
		t.Fatal("accepted wrong command")
	}
	wrong.Command = "true"
	wrong.Root = filepath.Join(root, "other")
	if _, _, err := ReconcilePlannedJob(db, cp, wrong, manager); err == nil {
		t.Fatal("accepted wrong worktree")
	}
	wrong.Root = root
	wrong.Fingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, _, err := ReconcilePlannedJob(db, cp, wrong, manager); err == nil {
		t.Fatal("accepted changed plan")
	}
}

func TestHandoffArtifactBetweenSessions(t *testing.T) {
	root := t.TempDir()
	receipt, err := PublishHandoffArtifact(root, "p7-handoff", "result", strings.NewReader("verified result"))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyHandoffArtifact(root, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishHandoffArtifact(root, "p7-handoff", "result", strings.NewReader("overwrite")); err == nil {
		t.Fatal("overwrote immutable artifact")
	}
	if err := os.WriteFile(filepath.Join(root, "p7-handoff", "result"), []byte("tampered result"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHandoffArtifact(root, receipt); err == nil {
		t.Fatal("accepted tampered artifact")
	}
	if _, err := PublishHandoffArtifact(root, "p7-handoff", "too-large", strings.NewReader(strings.Repeat("x", 8<<20+1))); err == nil {
		t.Fatal("accepted oversized artifact")
	}
	if _, err := PublishHandoffArtifact(root, "../escape", "bad", strings.NewReader("x")); err == nil {
		t.Fatal("accepted path traversal")
	}
}

func TestHandoffSymlinkDirectoryRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "workflow")); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishHandoffArtifact(root, "workflow", "payload", strings.NewReader("secret")); err == nil {
		t.Fatal("symlinked handoff directory accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "payload")); !os.IsNotExist(err) {
		t.Fatalf("escaped artifact: %v", err)
	}
}

func TestBuiltinDIAGProfile(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "gitverse.ru/dmitriy_1ebedev/diag")
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	s := New(p)

	resolved, err := s.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "builtin" || resolved.Profile.Name != "diag" {
		t.Fatalf("unexpected profile: %+v", resolved)
	}

	plan, err := s.Plan(root, "check")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Profile != "diag" || plan.Command != "make" || len(plan.Args) != 1 || plan.Args[0] != "vet" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.TimeoutSeconds != 600 || plan.OutputLimitBytes != 8*1024*1024 {
		t.Fatalf("unexpected limits: %+v", plan)
	}
}

func TestPlanFingerprintDetectsProfileDrift(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "gitverse.ru/dmitriy_1ebedev/diag")
	s := New(&policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	first, err := s.Plan(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Plan(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Fingerprint) != 64 || first.Fingerprint != again.Fingerprint {
		t.Fatalf("plan hash is missing or unstable: %q %q", first.Fingerprint, again.Fingerprint)
	}
	if err := os.Mkdir(filepath.Join(root, ".mcpshka"), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema":"mcpshka.project-profile/v1","name":"diag","match":{"module":"gitverse.ru/dmitriy_1ebedev/diag"},"workflows":{"test":{"command":"make","args":["test"],"timeout_seconds":60,"output_limit_bytes":1048576}}}`)
	if err := os.WriteFile(filepath.Join(root, ".mcpshka", "profile.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Plan(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == first.Fingerprint {
		t.Fatal("profile drift did not change pinned execution fingerprint")
	}
}

func TestMCPshkaBuiltinChecksUseBoundedPlanner(t *testing.T) {
	p := mcpshkaProfile()
	for _, name := range []string{"test", "check"} {
		w := p.Workflows[name]
		if w.Command != "python3" || len(w.Args) < 4 || w.Args[2] != "packaging/native/test-plan.py" {
			t.Fatalf("%s bypasses bounded test planner: %+v", name, w)
		}
		if w.Args[3] == "RELEASE" || w.Args[3] == "PRODUCTION" {
			t.Fatalf("%s must not use privileged/full release tier", name)
		}
	}
}

func TestRepositoryProfileOverridesBuiltin(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "gitverse.ru/dmitriy_1ebedev/diag")
	dir := filepath.Join(root, ".mcpshka")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	profile := Profile{
		Schema: SchemaV1,
		Name:   "diag-local",
		Match:  Match{Module: "gitverse.ru/dmitriy_1ebedev/diag", Markers: []string{"go.mod"}},
		Workflows: map[string]Workflow{
			"test": {Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 90, OutputLimitBytes: 1024 * 1024},
		},
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	s := New(p)
	resolved, err := s.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "repository" || resolved.Profile.Name != "diag-local" {
		t.Fatalf("unexpected override: %+v", resolved)
	}
	plan, err := s.Plan(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Command != "go" || plan.TimeoutSeconds != 90 {
		t.Fatalf("unexpected override plan: %+v", plan)
	}
}

func TestRepositoryProfileRejectsUnsafeShape(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "example.test/x")
	dir := filepath.Join(root, ".mcpshka")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema":"mcpshka.project-profile/v1","name":"bad","match":{"module":"example.test/x"},"workflows":{"x":{"command":"/bin/sh","args":["-c","true"],"timeout_seconds":5,"output_limit_bytes":1024}}}`)
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	if _, err := New(p).Resolve(root); err == nil {
		t.Fatal("accepted workflow command containing a path")
	}
}

func TestRepositoryProfileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "example.test/x")
	dir := filepath.Join(root, ".mcpshka")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "profile-target.json")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "profile.json")); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	if _, err := New(p).Resolve(root); err == nil {
		t.Fatal("accepted symlinked repository profile")
	}
}

func TestProfileMatchMustHold(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "example.test/x")
	dir := filepath.Join(root, ".mcpshka")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema":"mcpshka.project-profile/v1","name":"other","match":{"module":"example.test/y"},"workflows":{"test":{"command":"go","args":["test","./..."],"timeout_seconds":60,"output_limit_bytes":1048576}}}`)
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	if _, err := New(p).Resolve(root); err == nil {
		t.Fatal("accepted repository profile whose match does not fit project")
	}
}

func TestSealedScriptOnlyForTest(t *testing.T) {
	for _, c := range []struct {
		name, cmd string
		ok        bool
	}{
		{"test", "./tests/run_safe.sh", true},
		{"build", "./tests/run_safe.sh", false},
		{"test", "./../x.sh", false},
		{"test", "./a//b.sh", false},
		{"test", "./a b.sh", false},
	} {
		if got := sealedScript(c.name, c.cmd); got != c.ok {
			t.Errorf("%s %q: got %v", c.name, c.cmd, got)
		}
	}
}
