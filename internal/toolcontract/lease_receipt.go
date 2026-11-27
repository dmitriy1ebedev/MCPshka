package toolcontract

import (
	"encoding/json"
	"errors"

	"mcpshka/internal/projectlease"
)

// A broker successor stays sealed and may be inaccessible to the daemon's
// local lease service. Compare its cached identity with fresh broker evidence.
func ValidateRecoveryReceipt(path string, cached, observed any) error {
	decode := func(value, target any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, target)
	}
	var receipt projectlease.AcquireResult
	var current projectlease.Status
	if decode(cached, &receipt) != nil || decode(observed, &current) != nil {
		return errors.New("lease receipt evidence unreadable: reconcile")
	}
	old := receipt.Status
	if receipt.Token == "" || !old.Held || !current.Held || old.ProjectRoot != path || current.ProjectRoot != path || old.LeaseID == "" || old.Generation == 0 || old.ExecutionID == "" || old.Owner == "" || old.LeaseID != current.LeaseID || old.Generation != current.Generation || old.ExecutionID != current.ExecutionID || old.Owner != current.Owner || old.CommonGitDir != current.CommonGitDir {
		return errors.New("lease receipt belongs to a previous administrative holder: reconcile")
	}
	return nil
}
