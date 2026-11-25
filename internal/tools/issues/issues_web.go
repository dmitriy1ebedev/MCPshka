package issuetools

import (
	"context"
	"io"
	"mcpshka/internal/ownerweb"
	"net/http"
	"strings"

	"mcpshka/internal/issues"
	"mcpshka/internal/webpages"
)

// issuesAPI serves «Проблемы» under the owner's Face ID session of the
// connections portal (the caller already checked p.authed).
func (svc *Service) API(ctx context.Context, w http.ResponseWriter, r *http.Request, rest string) {
	fail := func(err error, code int) { ownerweb.JSON(w, map[string]string{"error": err.Error()}, code) }
	store := svc.Store()
	id, action, _ := strings.Cut(rest, "/")
	switch {
	case id == "" && r.Method == http.MethodGet:
		q := r.URL.Query()
		list, counts, err := store.List(issues.Filter{Status: q.Get("status"), Priority: q.Get("priority"), Project: q.Get("project"), Text: q.Get("text")})
		if err != nil {
			fail(err, http.StatusInternalServerError)
			return
		}
		ownerweb.JSON(w, map[string]any{"issues": list, "counts": counts}, http.StatusOK)
	case id != "" && action == "" && r.Method == http.MethodGet:
		i, err := store.Get(id)
		if err != nil {
			fail(err, http.StatusNotFound)
			return
		}
		ownerweb.JSON(w, map[string]any{"issue": i, "similar": svc.Store().Related(i)}, http.StatusOK)
	case id != "" && r.Method == http.MethodPost:
		var in struct {
			Status, Priority, Note, Append, Agent, Path, Evidence string
		}
		if err := ownerweb.ReadBody(r, &in); err != nil {
			fail(err, http.StatusBadRequest)
			return
		}
		var (
			out issues.Issue
			err error
		)
		switch action {
		case "update":
			out, err = store.Update(id, issues.Patch{Status: in.Status, Priority: in.Priority, Note: in.Note, Append: in.Append}, "owner")
		case "accept":
			evidence := strings.TrimSpace(in.Evidence)
			if evidence == "" {
				evidence = "owner"
			}
			out, err = store.Accept(id, evidence, in.Note, "owner", func(i issues.Issue, ev string) error { return svc.VerifyEvidence(i, ev, true) })
			if err == nil {
				if lesson := svc.lesson(out); lesson != "" && store.SetLesson(out.ID, lesson) == nil {
					out.Lesson = lesson
				}
			}
		case "dispatch":
			session, owner, werr := svc.ownerWindow(ctx)
			if werr != nil {
				agent := in.Agent
				_, _ = store.Update(id, issues.Patch{Dispatch: &agent, Note: "ждёт Face ID-окна для " + agent}, "owner")
				fail(werr, http.StatusConflict)
				return
			}
			out, _, err = svc.dispatch(ctx, session, owner, id, in.Agent, in.Path, "owner")
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			fail(err, http.StatusBadRequest)
			return
		}
		ownerweb.JSON(w, map[string]any{"issue": out}, http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (svc *Service) Page(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nonce := ownerweb.RandomHex(16)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, strings.Replace(webpages.Issues, "<script>", "<script nonce=\""+nonce+"\">", 1))
}
