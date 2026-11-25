package mcpserver

import (
	"mcpshka/internal/jobs"
	"mcpshka/internal/tools/ownerautomation"
)

type BrowserRunInput = ownerautomation.BrowserRunInput
type BrowserScreenshotInput = ownerautomation.BrowserScreenshotInput

var browserActs = ownerautomation.BrowserActs
var browserJobOptions = ownerautomation.BrowserJobOptions

func registerBrowserTools(r *toolRegistry, m *jobs.Manager) {
	ownerautomation.RegisterBrowser(r.install, m, ownerautomation.Deps{Store: r.store, Owner: uploadOwner, Inbox: artifactInbox(m)})
}
