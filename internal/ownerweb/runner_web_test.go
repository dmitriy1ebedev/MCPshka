package ownerweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcpshka/internal/runnerhub"
)

func runnerCall(method, path, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, connTestOrigin+path, strings.NewReader(body))
	r.Host = "h96.example"
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	rr := httptest.NewRecorder()
	runnerRoute(rr, r)
	return rr
}

// The machine side takes only a runner key; the token comes from the owner
// side behind the Face ID session.
func TestRunnerRoutes(t *testing.T) {
	p, _ := newConnTestPortal(t)
	old := Runners
	Runners = runnerhub.New(t.TempDir())
	t.Cleanup(func() { Runners = old })

	if rr := runnerCall(http.MethodGet, "/runner/agent.py", "", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), "mcpshka-runner") {
		t.Fatalf("agent.py: %d", rr.Code)
	}
	if rr := post(t, p, "/connections/api/runner-token", `{}`, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("token without Face ID: %d", rr.Code)
	}
	session := connLoginFlow(t, p)
	rr := post(t, p, "/connections/api/runner-token", `{}`, session)
	var tok struct{ Token string }
	_ = json.Unmarshal(rr.Body.Bytes(), &tok)
	if rr.Code != 200 || !strings.HasPrefix(tok.Token, "mrt_") {
		t.Fatalf("token: %d %s", rr.Code, rr.Body)
	}
	if rr := runnerCall(http.MethodPost, "/runner/poll", `{}`, "mrk_forged"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("forged key polled: %d", rr.Code)
	}
	rr = runnerCall(http.MethodPost, "/runner/register", `{"token":"`+tok.Token+`","name":"giga"}`, "")
	var reg struct{ ID, Key string }
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	if rr.Code != 200 || reg.Key == "" {
		t.Fatalf("register: %d %s", rr.Code, rr.Body)
	}
	if rr := runnerCall(http.MethodGet, "/runner/archive?job_id=rj-x", "", reg.Key); rr.Code != http.StatusForbidden {
		t.Fatalf("archive of a foreign job: %d", rr.Code)
	}
	rr = post(t, p, "/connections/api/runners", `{}`, session)
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "key_hash") || !strings.Contains(rr.Body.String(), "giga") {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	if rr := post(t, p, "/connections/api/runner-disable", `{"id":"`+reg.ID+`"}`, session); rr.Code != 200 {
		t.Fatalf("disable: %d", rr.Code)
	}
	if rr := runnerCall(http.MethodPost, "/runner/poll", `{}`, reg.Key); rr.Code != http.StatusUnauthorized {
		t.Fatalf("disabled runner polled: %d", rr.Code)
	}
}

func TestCleanupAPIOnlyListedPlaces(t *testing.T) {
	p, _ := newConnTestPortal(t)
	if rr := post(t, p, "/connections/api/cleanup-apply", `{"id":"go-cache"}`, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("cleanup without Face ID: %d", rr.Code)
	}
	session := connLoginFlow(t, p)
	var got []string
	p.broker = func(_ context.Context, family string, args ...string) (json.RawMessage, error) {
		got = append(got, family+" "+strings.Join(args, " "))
		return []byte(`{"id":"docker","freed":1}`), nil
	}
	if rr := post(t, p, "/connections/api/cleanup-apply", `{"id":"/"}`, session); rr.Code != http.StatusBadRequest || len(got) != 0 {
		t.Fatalf("unlisted cleanup: %d %v", rr.Code, got)
	}
	if rr := post(t, p, "/connections/api/cleanup-apply", `{"id":"docker"}`, session); rr.Code != 200 || got[0] != "cleanup apply docker" {
		t.Fatalf("docker cleanup: %d %v", rr.Code, got)
	}
}
