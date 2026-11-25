package approvedtask

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/rootbroker"
	"time"
)

// Continuation uses the same broker grant and pinned plan as a manual execute.
// It never grants permissions, renews an approval or launches another AI.
func continueApprovedTask(ctx context.Context, id string) (rootbroker.ApprovedResult, error) {
	cur, err := Call(ctx, "status", id)
	if err != nil {
		return cur, err
	}
	if !cur.AutoExecute || cur.State != "AWAITING_FACE_ID" || cur.ApprovalState != "authorized" {
		return cur, nil
	}
	if err := reachPreflight(ctx, cur.Plan); err != nil {
		return cur, err
	}
	return Call(ctx, "execute", id)
}

func Maintain() error {
	if !brokerclient.Enabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := brokerclient.Run(ctx, "approved-task", "pending")
	if err != nil {
		return err
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil || len(ids) > 1 {
		return errors.New("invalid approval continuation queue: HOLD")
	}
	for _, id := range ids {
		if _, err := continueApprovedTask(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// WebAuthn completion is the event trigger; durable maintenance recovers a lost event.
func ContinuePortal(ctx context.Context, raw []byte) {
	var result struct {
		Scope  string `json:"scope"`
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Scope != "approved_task" || result.TaskID == "" {
		return
	}
	_, _ = continueApprovedTask(ctx, result.TaskID)
}
