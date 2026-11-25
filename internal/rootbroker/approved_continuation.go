package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Only newly prepared tasks enter this durable outbox. Legacy approvals stay manual.
func queueApprovedAt(root string, r ApprovedResult) error {
	if !r.AutoExecute || r.State != "AWAITING_FACE_ID" {
		return nil
	}
	dir := filepath.Join(root, "continuations")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	name := filepath.Join(dir, r.ID)
	if raw, err := os.ReadFile(name); err == nil {
		if string(raw) != r.PlanHash {
			return errors.New("continuation fingerprint changed: HOLD")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(129)
	f.Close()
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) >= 128 {
		return errors.New("approval continuation capacity reached: HOLD")
	}
	return writeRootFile(name, []byte(r.PlanHash), 0600)
}

func expireAutomaticApprovedAt(root, id string, now time.Time) (ApprovedResult, error) {
	lock, err := openMutationLock(filepath.Join(root, id, "continuation.lock"))
	if err != nil {
		return ApprovedResult{}, err
	}
	defer lock.Close()
	r, err := loadApprovedAt(root, id)
	if err != nil {
		return r, err
	}
	if !r.AutoExecute || r.State != "AWAITING_FACE_ID" {
		return r, nil
	}
	state, _, _ := approvalView(r, readApprovalSignalsAt(filepath.Join(filepath.Dir(root), "grant-"+id+".json"), id), now)
	if state != "approval_expired" && state != "authorization_expired" {
		return r, nil
	}
	// Serialize expiry against the WebAuthn event before invalidating its challenge.
	pageLock, err := openMutationLock(filepath.Join(filepath.Dir(root), "ha-automations.lock"))
	if err != nil {
		return r, err
	}
	defer pageLock.Close()
	state, _, _ = approvalView(r, readApprovalSignalsAt(filepath.Join(filepath.Dir(root), "grant-"+id+".json"), id), now)
	if state != "approval_expired" && state != "authorization_expired" {
		return r, nil
	}
	r.State, r.Effect, r.FinishedAt = "EXPIRED", "none", &now
	if err := saveApprovedAt(root, r); err != nil {
		return r, err
	}
	for _, name := range []string{"passkey-" + id + ".json", "grant-" + id + ".json"} {
		if err := os.Remove(filepath.Join(filepath.Dir(root), name)); err != nil && !os.IsNotExist(err) {
			return r, err
		}
	}
	return r, nil
}

func pendingApproved(ctx context.Context) ([]string, error) {
	ids := []string{}
	dir := filepath.Join(approvedRoot, "continuations")
	f, err := os.Open(dir)
	if os.IsNotExist(err) {
		return ids, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(129)
	f.Close()
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 128 {
		return nil, errors.New("approval continuation scan exceeds capacity: HOLD")
	}
	active := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := entry.Name()
		if !approvedID.MatchString(id) || entry.IsDir() {
			return nil, errors.New("invalid continuation identity: HOLD")
		}
		raw, _, err := readFixedRegular(filepath.Join(dir, id))
		if err != nil {
			return nil, err
		}
		r, err := loadApproved(id)
		if err != nil {
			return nil, err
		}
		if !r.AutoExecute || string(raw) != r.PlanHash {
			return nil, errors.New("continuation plan mismatch: HOLD")
		}
		r, err = approvedStatus(ctx, id)
		if err != nil {
			return nil, err
		}
		if r.terminal() {
			if err := os.Remove(filepath.Join(dir, id)); err != nil {
				return nil, err
			}
			continue
		}
		if r.State == "RUNNING" {
			active = true
			continue
		}
		if r.ApprovalState == "consumed_unrecorded" || r.ApprovalState == "invalid_authorization" {
			return nil, errors.New("approval effect UNKNOWN: reconcile original task " + id)
		}
		if r.State == "AWAITING_FACE_ID" && r.ApprovalState == "authorized" {
			ids = append(ids, id)
		}
	}
	if active {
		return []string{}, nil
	}
	// Resource-bound: one newly authorized worker per maintenance pass.
	if len(ids) > 1 {
		ids = ids[:1]
	}
	return ids, nil
}

func loadApprovedAt(root, id string) (ApprovedResult, error) {
	var r ApprovedResult
	if !approvedID.MatchString(id) {
		return r, errors.New("invalid approved task ID")
	}
	f, err := os.Open(filepath.Join(root, id, "task.json"))
	if err != nil {
		return r, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err != nil {
		return r, err
	}
	if len(raw) > 128<<10 || json.Unmarshal(raw, &r) != nil || r.ID != id || !validHash(r.PlanHash) || approvedHash(r.Plan) != r.PlanHash {
		return ApprovedResult{}, errors.New("approved task identity/plan invalid: HOLD")
	}
	return r, nil
}
func saveApprovedAt(root string, r ApprovedResult) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeRootFile(filepath.Join(root, r.ID, "task.json"), raw, 0600)
}

// No automatic operation changes the plan or grants. An invalid timestamp fails closed.
func validApprovalTime(t, now time.Time) bool {
	return !t.IsZero() && !t.After(now) && now.Sub(t) <= connectorGrantTTL
}
