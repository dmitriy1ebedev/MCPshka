package mcpserver

import (
	"encoding/json"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatCompatDoesNotPersistHolderToken(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	if _, e := statestore.Open(filepath.Join(state, "state.db")); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600)
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	m, e := jobs.New(filepath.Join(state, "jobs"), p)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close(time.Second)
	s := projectlease.New(filepath.Join(state, "leases"), p)
	a, e := s.AcquireTracked(root, "owner", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(ProjectLeaseHeartbeatInput{Path: root, Token: a.Token})
	in := ExecStartInput{Command: "mcpshka", Args: []string{"lease-heartbeat", string(raw)}, Workdir: root, IdempotencyKey: "heartbeat"}
	j, e := leaseHeartbeatCompat(m, s, in)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(strings.Join(j.Args, " "), a.Token) {
		t.Fatal("token in job argv")
	}
	o, e := m.Read(j.ID, "stdout", 0, 4096)
	if e != nil || strings.Contains(o.Text, a.Token) {
		t.Fatal("token in receipt", e)
	}
	status, e := s.Status(root)
	if e != nil || !status.HeartbeatAt.After(a.Status.HeartbeatAt) || status.Reclaimable {
		t.Fatal("heartbeat evidence", e)
	}
	view := leaseRecoveryStatus(status, jobs.ProjectActivity{})
	if view.WriterScopeState != "busy" || view.ParallelWork != "independent_project_or_sibling_worktree" {
		t.Fatal("parallel scope not advertised")
	}
	if _, e = s.Release(root, a.Token); e != nil {
		t.Fatal(e)
	}
	if _, e = leaseHeartbeatCompat(m, s, in); e == nil {
		t.Fatal("released lease resurrected")
	}
}
