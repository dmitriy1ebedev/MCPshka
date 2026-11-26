package projectdrift

import (
	"context"
	"mcpshka/internal/releasequeue"
	"mcpshka/internal/statestore"
	"time"
)

type Link struct {
	State      string    `json:"state"`
	Identity   string    `json:"identity"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}
type Chain struct {
	Source      Link `json:"source"`
	CI          Link `json:"ci_sha"`
	Integration Link `json:"integration"`
	Candidate   Link `json:"candidate"`
	Installed   Link `json:"installed"`
	Rollback    Link `json:"rollback"`
}

// SourceChain reports recorded lineage; missing or unrelated evidence stays UNKNOWN.
func SourceChain(ctx context.Context, db *statestore.DB, root, head, testJob string, testPass bool, now time.Time) Chain {
	unknown := Link{State: "UNKNOWN"}
	c := Chain{Source: Link{"OBSERVED", head, "git:" + root, now}, CI: unknown, Integration: unknown, Candidate: unknown, Installed: unknown, Rollback: unknown}
	if head == "" {
		c.Source = unknown
		return c
	}
	if testJob != "" && testPass {
		c.CI = Link{"PASS", head, "project_test:job/" + testJob, now}
	}
	// Queue records belong to MCPshka only; other projects must not inherit them.
	if root != "/root/repos/mcpshka" {
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	items, err := releasequeue.Load(ctx, db)
	if err != nil {
		return c
	}
	for _, q := range items {
		if q.Commit != head {
			continue
		}
		ref := "release_queue_status:" + q.Version
		view := releasequeue.View{Stage: q.Stage, PreviousVersion: q.PreviousVersion}
		stages := releasequeue.StagesOf(&view, "", q.Version)
		if stages.Integrated {
			c.Integration = Link{"RECORDED", q.Commit, ref, q.UpdatedAt}
		}
		if q.BuildPermit != "" && q.SourceDigest != "" {
			c.Candidate = Link{"RECORDED", q.SourceDigest, ref + ":build/" + q.BuildPermit, q.UpdatedAt}
		}
		if q.Stage == "DONE" {
			c.Installed = Link{"RECORDED", q.Version, ref + ":project_verify", q.UpdatedAt}
			if q.PreviousVersion != "" {
				c.Rollback = Link{"RECORDED", q.PreviousVersion, ref + ":rollback", q.UpdatedAt}
			}
		}
	}
	return c
}
