package mcpserver

import (
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/ownerautomation"
)

type DocRun = ownerautomation.DocRun

func registerDocTools(r *toolRegistry, m *jobs.Manager) {
	ownerautomation.RegisterDoc(r.install, m, ownerautomation.Deps{Store: r.store, Owner: uploadOwner, Inbox: artifactInbox(m)})
}
