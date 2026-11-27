package forgeci

import (
	"context"
	"mcpshka/internal/browser"
	"mcpshka/internal/runnerhub"
	"net/url"
	"strings"
)

var browserRun = browser.Run

func wakeGigaIDE(ctx context.Context, workspace string, run func(context.Context, browser.Spec) browser.Result) ([]byte, int, error) {
	u, err := url.Parse(workspace)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Fragment != "" {
		return wakeReport("gigaide", "refused", "WORKSPACE_URL_NOT_CONFIGURED")
	}
	// The operator configures this URL; never take navigation from tool input.
	spec := browser.Spec{ID: strings.TrimPrefix(runnerhub.NewJobID(), "rj-"), Site: u.Hostname(),
		ProcessTimeoutSeconds: 90, Steps: []browser.Step{
			{Action: "goto", URL: workspace, TimeoutMS: 60000},
			{Action: "wait", Selector: ".monaco-workbench", TimeoutMS: 15000},
		}}
	result := run(ctx, spec)
	login := result.FailureStage == "auth"
	for _, step := range result.Steps {
		login = login || step.FailureStage == "auth"
	}
	for _, raw := range []string{result.FinalURL, result.Title} {
		lower := strings.ToLower(raw)
		login = login || strings.Contains(lower, "login") || strings.Contains(lower, "sign in") || strings.Contains(lower, "/auth/")
	}
	if login {
		return wakeReport("gigaide", "refused", "LOGIN_NEEDED: владелец один раз входит на странице /connections/live?site=gigaide")
	}
	if result.State != "done" {
		return wakeReport("gigaide", "refused", "WORKSPACE_WAKE_UNVERIFIED")
	}
	return wakeReport("gigaide", "accepted", "WORKSPACE_OPENED_RUNNER_REGISTRATION_PENDING")
}
