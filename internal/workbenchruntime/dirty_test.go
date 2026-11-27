package workbenchruntime

import (
	"testing"

	"mcpshka/internal/failure"
)

func TestDirtyTreeIsTypedNoEffect(t *testing.T) {
	err := dirtyTree("/root/repos/lane", nil)
	d := failure.Details(err)
	if !failure.IsNoEffect(err) || d == nil || d.Code != "WORKTREE_DIRTY" || d.NextSafeAction.Tool != "project_commit" {
		t.Fatalf("dirty tree must be a typed no-effect refusal: %#v", d)
	}
}
