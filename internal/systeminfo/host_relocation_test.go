package systeminfo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"mcpshka/internal/policy"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelocatePlanPinsIdentityButCannotApply(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	data := []byte("keep every byte")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}}
	plan, err := PlanHostRelocate(context.Background(), p, source, filepath.Join(root, "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256(data)
	if plan.SourceSHA256 != hex.EncodeToString(sha[:]) || plan.SourceInode == 0 {
		t.Fatalf("no source evidence: %+v", plan)
	}
	if plan.State != "HOLD" || len(plan.Blockers) < 8 || plan.NextStep == "" {
		t.Fatalf("unverified move authorized: %+v", plan)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("source moved by preview")
	}
}
func TestRelocateRejectsAliasAndRecursiveTargets(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}}
	if _, err := PlanHostRelocate(context.Background(), p, dir, filepath.Join(dir, "nested")); err == nil {
		t.Fatal("nested target accepted")
	}
	if _, err := PlanHostRelocate(context.Background(), p, dir, dir); err == nil {
		t.Fatal("same path accepted")
	}
	if err := os.Symlink(dir, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanHostRelocate(context.Background(), p, filepath.Join(root, "alias"), filepath.Join(root, "target")); err == nil {
		t.Fatal("symlink source accepted")
	}
	if _, err := PlanHostRelocate(context.Background(), p, dir, filepath.Join(root, "not", "created")); err == nil {
		t.Fatal("unavailable target parent accepted")
	}
	if _, err := PlanHostRelocate(context.Background(), p, dir, "/etc/blocked"); err == nil || !strings.Contains(err.Error(), "policy-approved") {
		t.Fatalf("escape not explained: %v", err)
	}
}
