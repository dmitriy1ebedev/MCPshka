package rootbroker

import (
	"encoding/json"
	"errors"
	"path/filepath"
)

const rootTaskPlanVersion = 1

type rootTaskApprovalHead struct {
	PlanVersion int    `json:"plan_version"`
	TaskID      string `json:"task_id"`
	StagedSHA   string `json:"staged_sha"`
}

func validateRootApproval(p rootTaskPermit, h rootTaskApprovalHead) error {
	if p.PlanVersion != rootTaskPlanVersion || h.PlanVersion != rootTaskPlanVersion ||
		!validHex(h.TaskID, 32) || !validHash(h.StagedSHA) ||
		h.TaskID != p.ID || h.StagedSHA != p.StagedSHA {
		return errors.New("approval superseded or plan version/content changed")
	}
	return nil
}

// Called while holding the shared HA mutation lock.
func saveRootApproval(p rootTaskPermit) error {
	return writeRootOnly(filepath.Join(taskRoot, "ha-active-approval.json"),
		rootTaskApprovalHead{PlanVersion: rootTaskPlanVersion, TaskID: p.ID, StagedSHA: p.StagedSHA}, false)
}

// The same lock protects the head check and the subsequent config replacement.
func requireRootApproval(p rootTaskPermit) error {
	raw, _, err := readFixedRegular(filepath.Join(taskRoot, "ha-active-approval.json"))
	if err != nil {
		return errors.New("current approval unavailable; prepare a new task")
	}
	var head rootTaskApprovalHead
	if json.Unmarshal(raw, &head) != nil {
		return errors.New("current approval record invalid")
	}
	return validateRootApproval(p, head)
}
