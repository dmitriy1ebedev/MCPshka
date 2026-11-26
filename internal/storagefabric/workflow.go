package storagefabric

import (
	"errors"
	"path/filepath"
	"time"
)

const BackupSchema = "mcpshka.backup/v1"

type Workflow struct {
	RestorePlan    *RestorePlan `json:"restore_plan,omitempty"`
	Schema         string       `json:"schema"`
	Scope          string       `json:"scope"`
	Phase          string       `json:"phase"`
	UpdatedAt      time.Time    `json:"updated_at"`
	RetryPolicy    string       `json:"retry_policy"`
	Postconditions []string     `json:"postconditions"`
}

type RestorePlan struct {
	Schema              string `json:"schema"`
	Scope               string `json:"scope"`
	BackupID            string `json:"backup_id"`
	SHA256              string `json:"sha256"`
	StagingPath         string `json:"staging_path"`
	ApprovalRequired    bool   `json:"approval_required"`
	WriterFenceRequired bool   `json:"writer_fence_required"`
}

// A checkpoint records facts, never permission to replay external I/O.
func (e Engine) checkpoint(op *operation, phase string, postconditions []string) error {
	w := op.Result.Workflow
	if w == nil {
		target := op.Result.ID
		provider := op.Request.Provider
		if op.Request.Action == "backup_verify" {
			target = op.Request.Destination
			source, err := e.load(target, op.Request.Owner)
			if err != nil {
				return err
			}
			provider = source.Request.Provider
		}
		w = &Workflow{Schema: BackupSchema, Scope: "backup/" + provider + "/" + target, RetryPolicy: "same_id_receipt_only; UNKNOWN_requires_reconciliation"}
	}
	if w.Schema != BackupSchema {
		return errors.New("backup workflow schema incompatible: HOLD")
	}
	w.Phase, w.UpdatedAt, w.Postconditions = phase, time.Now().UTC(), postconditions
	if phase == "verified" && op.Request.Action == "backup_create" {
		w.RestorePlan = &RestorePlan{Schema: "mcpshka.restore-plan/v1", Scope: "staged_restore/" + op.Result.ID, BackupID: op.Result.ID, SHA256: op.Result.SHA256, StagingPath: filepath.Join(e.Dir, "staging", "restore-"+op.Result.ID), ApprovalRequired: true, WriterFenceRequired: true}
	}
	op.Result.Workflow = w
	return e.save(*op)
}
