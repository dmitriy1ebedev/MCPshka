package jobs

import (
	"errors"
	"strings"
	"testing"

	"mcpshka/internal/failure"
)

// DIAG ACTIVE-04 / MCP-121: a write refused because a test runs is a typed,
// effect-free refusal naming the job, never FENCED_UNKNOWN.
func TestProjectBusyIsTypedAndNamesJob(t *testing.T) {
	err := projectBusy(&Job{ID: "mw1rarf6-8", State: Running})
	var e *failure.Error
	if !errors.As(err, &e) || e.Class != failure.OwnerBusy || e.Code != "PROJECT_BUSY" {
		t.Fatalf("untyped refusal: %v", err)
	}
	if e.NextSafeAction.Tool != "job_wait" || e.NextSafeAction.Arguments["job_id"] != "mw1rarf6-8" || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("refusal lacks job route: %v", err)
	}
	// MCP-136: the receipt layer releases the key only for no-effect errors.
	if !failure.IsNoEffect(err) {
		t.Fatalf("PROJECT_BUSY must be no-effect so the same key can be repeated: %v", err)
	}
}
