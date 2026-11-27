package mcpserver

import (
	"context"

	"mcpshka/internal/agents"
	"mcpshka/internal/brokerclient"
)

// leaseHeldBy: an unreadable lease counts as held, so doubt never frees it.
func (r *toolRegistry) leaseHeldBy(path, owner string) bool {
	st, err := r.leases.Status(path)
	return err != nil || (st.Held && st.Owner == owner)
}

// staleLeaseBroker runs one admin-session broker operation; tests swap it.
var staleLeaseBroker = func(ctx context.Context, args ...string) ([]byte, error) {
	return brokerclient.Run(ctx, "admin-session", args...)
}

// releaseTaskLease frees the lease an ended task left behind through the
// broker path of project_lease_admin_stale_preview/release (root SQLite owner,
// same proof: its agent_task ended, no jobs), so cancel/resume need no manual cleanup.
func (r *toolRegistry) releaseTaskLease(ctx context.Context, session, owner string, t agents.Task) string {
	if (t.State != "cancelled" && t.State != "failed" && t.State != "quota" && t.State != "retry_scheduled" && t.State != "interrupted" && t.State != "unknown") || !r.leaseHeldBy(t.Path, "agent-task:"+t.ID) {
		return "not held"
	}
	// The pinned plan goes back to the broker byte for byte.
	plan, err := staleLeaseBroker(ctx, "preview-stale-lease", session, owner, t.Path)
	if err == nil {
		_, err = staleLeaseBroker(ctx, "release-stale-lease", session, owner, string(plan))
	}
	if err != nil {
		return "kept: " + err.Error()
	}
	return "released"
}
