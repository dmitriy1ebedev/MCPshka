package freellm

import (
	"context"

	"mcpshka/internal/llmgateway"
	"mcpshka/internal/runoutcome"
)

type ScheduledReport struct {
	runoutcome.Result
	Fixes []Fix `json:"fixes"`
}

// Outcome is consumed by the native job runner when persisting the receipt.
func (r ScheduledReport) Outcome() runoutcome.Result { return r.Result }

func (c *Conveyor) RunScheduled(ctx context.Context) ScheduledReport {
	processed := 0
	blocker, next := "", ""
	fixes, err := c.run(ctx, "night", func(before, after Fix) {
		if after.Commit != "" && after.Commit != before.Commit || after.Test != "" && after.Test != before.Test || after.State == "green" && before.State != "green" {
			processed++
		} else if after.State == "blocked" && after.Reason != "" {
			blocker, next = after.Reason, "inspect the blocked fix before retrying its card"
		} else if blocker == "" {
			blocker, next = "existing_fix_"+after.State, "inspect the existing fix and its test receipt"
		}
	})
	if err != nil {
		blocker, next = llmgateway.Redact(err.Error()).Text, "inspect the scheduled run and existing fix receipts before retrying"
	}
	return ScheduledReport{Result: runoutcome.Resolve(processed, len(fixes) > 0, blocker, next), Fixes: fixes}
}
