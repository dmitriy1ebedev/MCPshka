package mcpserver

import (
	"context"
	"time"

	"mcpshka/internal/agents"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/memory"
	"mcpshka/internal/notify"
	"mcpshka/internal/releasequeue"
	"mcpshka/internal/rootbroker"
)

// registerReleaseQueue starts the one production autorelease watcher.
func registerReleaseQueue(r *toolRegistry, m *jobs.Manager, git *gitops.Service) {
	if db := m.Database(); brokerclient.Enabled() && db != nil && releasequeue.Running.CompareAndSwap(false, true) {
		w := &releasequeue.Watcher{DB: db, Log: m.AuditLogger(), Now: time.Now, Installed: installedVersion, ProviderAuth: agents.Health,
			Memory: func() (*memory.Store, error) { return memory.Open(m.StateDir()) },
			Call: func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
				return r.call(withRoute(ctx, "release_queue"), name, args)
			}}
		w.Admit = func(ctx context.Context, previousVersion string) error {
			return releasequeue.AdmitEnvironment(ctx, m, Version, previousVersion)
		}
		w.DiscoveryIdle = func(path string) bool {
			lease, err := r.leases.Status(path)
			return err == nil && !lease.Held && !lease.ReconciliationRequired && m.ProjectActivityReadOnly(path, 1).Count == 0
		}
		w.Challenges = func(ctx context.Context, c notify.AdminChallenge) (string, error) {
			out, err := runAdminSession[rootbroker.AdminSessionView](ctx, "status", c.ID, c.Owner)
			return out.State, err
		}
		w.Tidy = func(_ context.Context, branch string) []string { return tidyRelease(r, git, branch) }
		go w.Run()
	}
}

// tidyRelease clears every merged clean worktree and branch nobody holds
// (gitops.TidyMerged), then packs git.
func tidyRelease(r *toolRegistry, git *gitops.Service, _ string) []string {
	notes := append(r.leases.AutoReleaseNotes(time.Now()), git.TidyMerged(releasequeue.Repo, func(dir string) string {
		if st, err := r.leases.Status(dir); err == nil && st.Held {
			return st.Owner
		}
		return ""
	})...)
	if err := git.Compact(releasequeue.Repo); err != nil {
		return append(notes, "git gc: "+err.Error())
	}
	return append(notes, "git packed")
}
