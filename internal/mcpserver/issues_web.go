package mcpserver

import (
	"context"
	"net/http"
)

func issuesAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, rest string) {
	issueService.API(ctx, w, r, rest)
}
func issuesPage(w http.ResponseWriter, r *http.Request) { issueService.Page(w, r) }
