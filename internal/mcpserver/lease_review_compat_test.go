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

func TestLeaseReviewCompatIsReadOnlyAndBounded(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	m, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(time.Second)
	s := projectlease.New(filepath.Join(state, "leases"), p)
	a, err := s.AcquireTracked(root, "chatgpt-diag-p02-20261009", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	in := ExecStartInput{Command: "mcpshka", Args: []string{"lease-review", root}, Workdir: root}
	j, err := leaseReviewCompat(m, s, in)
	if err != nil {
		t.Fatal(err)
	}
	o, err := m.Read(j.ID, "stdout", 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(o.Text, a.Token) {
		t.Fatal("holder token exposed")
	}
	var review projectlease.AdministrativeReview
	if err := json.Unmarshal([]byte(o.Text), &review); err != nil {
		t.Fatal(err)
	}
	if review.State != "UNKNOWN" || review.FencingState != "not_verified" {
		t.Fatalf("unverified owner: %+v", review)
	}
	if _, err := s.ValidateHolder(root, a.Token); err != nil {
		t.Fatal("review changed lease", err)
	}
	in.Env = map[string]string{"OVERRIDE": "yes"}
	if _, err := leaseReviewCompat(m, s, in); err == nil {
		t.Fatal("override accepted")
	}
	in.Env = nil
	in.Args = []string{"lease-review", root, "transfer"}
	if _, err := leaseReviewCompat(m, s, in); err == nil {
		t.Fatal("mutation accepted")
	}
}
