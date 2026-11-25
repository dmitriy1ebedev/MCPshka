package storagefabric

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupWorkflowCheckpointAndRestoreRemainStaged(t *testing.T) {
	dir := privateTempDir(t)
	e := Engine{Dir: dir}
	id := strings.Repeat("a", 32)
	op := operation{Request: Request{Action: "backup_create", Owner: "owner", Provider: "drive"}, Result: Result{ID: id, State: "verified", SHA256: strings.Repeat("b", 64), Entries: json.RawMessage(`{"version":1,"bundles":[]}`)}}
	if err := e.checkpoint(&op, "verified", []string{"download_hash_aead_manifest"}); err != nil {
		t.Fatal(err)
	}
	cold, err := e.load(id, "owner")
	if err != nil || cold.Result.Workflow.Schema != "mcpshka.backup/v1" || cold.Result.Workflow.Scope != "backup/drive/"+id || cold.Result.Workflow.Phase != "verified" {
		t.Fatal(cold, err)
	}
	if cold.Result.Workflow.RestorePlan == nil || cold.Result.Workflow.RestorePlan.Schema != "mcpshka.restore-plan/v1" || !cold.Result.Workflow.RestorePlan.ApprovalRequired || !cold.Result.Workflow.RestorePlan.WriterFenceRequired {
		t.Fatal("versioned live-restore boundary not durable", cold)
	}
	before, _ := os.ReadFile(e.opPath(id))
	planned, err := e.restorePlan(context.Background(), Request{ID: id, Owner: "owner"})
	after, _ := os.ReadFile(e.opPath(id))
	if err != nil || planned.State != "planned" || planned.LocalPath != filepath.Join(dir, "staging", "restore-"+id) || string(before) != string(after) {
		t.Fatal(planned, err)
	}
	if _, err := os.Stat(planned.LocalPath); !os.IsNotExist(err) {
		t.Fatal("restore plan wrote staging or production")
	}
}

func TestBackupWorkerRejectsFutureContractBeforeWriting(t *testing.T) {
	e := Engine{Dir: privateTempDir(t)}
	id := strings.Repeat("f", 32)
	op := operation{Request: Request{Action: "backup_create", Owner: "owner"}, Result: Result{ID: id, State: "pending", Workflow: &Workflow{Schema: "mcpshka.backup/v999"}}}
	if err := e.save(op); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(e.opPath(id))
	if err := e.RunJob(context.Background(), id); err == nil {
		t.Fatal("future contract executed")
	}
	after, _ := os.ReadFile(e.opPath(id))
	if string(before) != string(after) {
		t.Fatal("incompatible active job mutated its pinned contract")
	}
}

func TestStorageHealthUnknownAndVerifiedEvidence(t *testing.T) {
	e := Engine{Dir: privateTempDir(t), Vault: Vault{Dir: privateTempDir(t)}}
	result, err := e.Do(context.Background(), Request{Action: "health", Owner: "observer"})
	if err != nil {
		t.Fatal(err)
	}
	var h Health
	if json.Unmarshal(result.Entries, &h) != nil || h.State == "ok" || h.BackupFreshness != "unknown" || h.VerifiedReplicaCount == nil || *h.VerifiedReplicaCount != 0 || h.LastRestoreDrill != nil || h.StorageQuota != nil {
		t.Fatal("invented healthy backup", h)
	}
	id := strings.Repeat("a", 32)
	op := operation{Request: Request{Action: "backup_create", Owner: "owner", Provider: "drive"}, Result: Result{ID: id, State: "verified", SHA256: strings.Repeat("b", 64)}}
	if err := e.checkpoint(&op, "verified", []string{"download_hash_aead_manifest"}); err != nil {
		t.Fatal(err)
	}
	result, err = e.Do(context.Background(), Request{Action: "health", Owner: "observer"})
	if json.Unmarshal(result.Entries, &h) != nil || err != nil || h.BackupFreshness != "fresh" || *h.VerifiedReplicaCount != 1 || h.State == "ok" {
		t.Fatal("verification or unknown provider masked", h, err)
	}
	op.Result.Workflow.UpdatedAt = time.Now().Add(-48 * time.Hour)
	if err = e.save(op); err != nil {
		t.Fatal(err)
	}
	result, err = e.Do(context.Background(), Request{Action: "health", Owner: "observer"})
	if json.Unmarshal(result.Entries, &h) != nil || err != nil || h.BackupFreshness != "stale" {
		t.Fatal(h, err)
	}
}

// privateTempDir is a storage directory as production requires it (0700,
// owned): CI runners may create test temp dirs group/world-accessible.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
