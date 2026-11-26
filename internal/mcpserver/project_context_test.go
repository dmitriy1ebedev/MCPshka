package mcpserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"mcpshka/internal/deploy"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/statestore"
)

func TestProjectContextFindsNextItemProfilesAndPreservesDirtyWork(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"go.mod": "module mcpshka\n\ngo 1.26\n", "ROADMAP.md": "## P7 — Workflow\n- [ ] handoff\n## P8 — Health\n- [ ] health\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Canary", "-c", "user.email=canary@localhost", "commit", "-qm", "seed"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("seed: %s %v", out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "unknown.txt"), []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	state := t.TempDir()
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := projectContext(m, ProjectContextInput{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if got.Trust != "UNTRUSTED" || got.CurrentItem == nil || got.CurrentItem.ID != "P7" || !got.Git.Dirty || got.Profile != "mcpshka" || got.Test == nil || got.Build == nil || got.DeployProfile != "" {
		t.Fatalf("context missing or overclaims capabilities: %+v", got)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module nested\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := projectContext(m, ProjectContextInput{Path: nested}); err != nil || got.Root != root || got.CurrentItem == nil || got.CurrentItem.ID != "P7" {
		t.Fatalf("nested Git ownership lost: %+v %v", got, err)
	}
	scan, err := roadmapscan.Load(filepath.Join(root, "ROADMAP.md"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ROADMAP.md"), []byte("## P7 — Changed\n- [ ] item\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectContext(m, ProjectContextInput{Path: root, ExpectedRoadmapSHA: scan.SHA256}); err == nil {
		t.Fatal("stale roadmap pin accepted")
	}
	b, err := os.ReadFile(filepath.Join(root, "unknown.txt"))
	if err != nil || string(b) != "preserve me" {
		t.Fatal("read context changed unknown work")
	}
	if err := os.Remove(filepath.Join(root, "ROADMAP.md")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "ROADMAP.md"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := projectContext(m, ProjectContextInput{Path: root}); err == nil {
		t.Fatal("FIFO roadmap accepted")
	}
}

func TestProjectContextDeploymentReconciliationHOLDDoesNotHideEvidence(t *testing.T) {
	out := ProjectContext{}
	observed := deploy.Reconciliation{Trust: "UNTRUSTED", Pending: false}
	applyDeploymentReconciliation(&out, observed, errors.New("historical deployment fence pin mismatch: HOLD"))
	if !out.DeploymentPending || !out.DeploymentReconciliation.Pending ||
		!out.DeploymentReconciliation.ReconciliationRequired ||
		out.DeploymentReconciliation.Trust != "UNTRUSTED" ||
		len(out.Warnings) != 1 || out.Warnings[0] != "deployment_reconciliation_hold" {
		t.Fatalf("broken historical fence improperly treated as idle: %+v", out)
	}
	// The failure is surfaced only as a generic, bounded warning; no privileged
	// receipt, root path, or credential is copied into read-only handoff output.
	out = ProjectContext{}
	applyDeploymentReconciliation(&out, deploy.Reconciliation{Trust: "UNTRUSTED", Pending: true, ReconciliationRequired: true}, nil)
	if !out.DeploymentPending || len(out.Warnings) != 0 {
		t.Fatalf("known pending state lost: %+v", out)
	}
	out = ProjectContext{}
	applyDeploymentReconciliation(&out, deploy.Reconciliation{Trust: "UNTRUSTED", Pending: false}, nil)
	if out.DeploymentPending || len(out.Warnings) != 0 {
		t.Fatalf("verified idle state was blocked: %+v", out)
	}
}
