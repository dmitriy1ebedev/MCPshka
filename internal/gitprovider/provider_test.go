package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func testClient(t *testing.T, site Site, handler http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c, err := New(site)
	if err != nil {
		t.Fatal(err)
	}
	c.base = ts.URL
	c.token = func(Site) (string, error) { return "test-secret-do-not-echo", nil }
	return c
}
func repoJSON(owner, name string, private bool) string {
	return fmt.Sprintf("{\"name\":%q,\"full_name\":%q,\"private\":%t,\"html_url\":\"https://example.invalid/%s/%s\",\"default_branch\":\"main\"}", name, owner+"/"+name, private, owner, name)
}
func TestCreatePrivateAndIdempotent(t *testing.T) {
	var mu sync.Mutex
	created := false
	postCount := 0
	c := testClient(t, GitHub, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret-do-not-echo" {
			t.Error("missing scoped bearer")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/user":
			w.Write([]byte(`{"login":"owner"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/owner/lane":
			mu.Lock()
			v := created
			mu.Unlock()
			if !v {
				w.WriteHeader(404)
				return
			}
			w.Write([]byte(repoJSON("owner", "lane", true)))
		case r.Method == "POST" && r.URL.Path == "/user/repos":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload["private"] != true || payload["auto_init"] != false {
				t.Error("unsafe create flags", payload)
			}
			mu.Lock()
			created = true
			postCount++
			mu.Unlock()
			w.WriteHeader(201)
			w.Write([]byte(repoJSON("owner", "lane", true)))
		default:
			w.WriteHeader(404)
		}
	})
	for i := 0; i < 2; i++ {
		r, err := c.Create(context.Background(), "owner", "lane", "", true)
		if err != nil {
			t.Fatal(err)
		}
		if r.Repository.FullName != "owner/lane" || !r.Repository.Private {
			t.Fatalf("not private: %+v", r)
		}
		if i == 0 && !r.Created {
			t.Error("first call did not create")
		}
		if i == 1 && r.Created {
			t.Error("second call duplicated resource")
		}
	}
	if postCount != 1 {
		t.Errorf("POST occurred %d times", postCount)
	}
}
func TestAmbiguousCreateReconcilesOnlyByGET(t *testing.T) {
	var mu sync.Mutex
	exists := false
	postCount := 0
	c := testClient(t, GitVerse, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "gitverse") {
			t.Error("wrong GitVerse contract")
		}
		switch r.URL.Path {
		case "/user":
			w.Write([]byte(`{"login":"owner"}`))
		case "/repos/owner/demo":
			mu.Lock()
			yes := exists
			mu.Unlock()
			if !yes {
				w.WriteHeader(404)
				return
			}
			w.Write([]byte(repoJSON("owner", "demo", true)))
		case "/user/repos":
			mu.Lock()
			exists = true
			postCount++
			mu.Unlock()
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("cannot simulate disconnect")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		default:
			w.WriteHeader(404)
		}
	})
	result, err := c.Create(context.Background(), "owner", "demo", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reconciled || result.State != "RECONCILED" || postCount != 1 {
		t.Fatalf("bad reconcile: %+v posts=%d", result, postCount)
	}
}
func TestMissingAuthorityNoSideEffect(t *testing.T) {
	hit := false
	c := testClient(t, GitHub, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(401)
		w.Write([]byte("test-secret-do-not-echo"))
	})
	_, err := c.Create(context.Background(), "owner", "demo", "", true)
	if err == nil || !hit {
		t.Fatal("401 should reject the operation")
	}
	if strings.Contains(err.Error(), "test-secret") {
		t.Fatal("provider response leaked secret")
	}
}
func TestRefusesRedirectsAndMalformedNames(t *testing.T) {
	c := testClient(t, GitHub, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://evil.invalid/token")
		w.WriteHeader(302)
	})
	_, err := c.WhoAmI(context.Background())
	var h *HTTPError
	if !errors.As(err, &h) || h.Status != 302 {
		t.Fatalf("redirect followed or not rejected: %v", err)
	}
	for _, name := range []string{"..", "../repo", "my repo", "foo.git", ""} {
		if _, err := c.Create(context.Background(), "owner", name, "", true); err == nil {
			t.Errorf("accepted name %q", name)
		}
	}
}
func TestMosHubProjectInspection(t *testing.T) {
	c := testClient(t, MosHub, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") == "" {
			t.Error("missing GitLab token")
		}
		if r.URL.Path != "/api/v4/projects/owner%2Flane" && r.URL.EscapedPath() != "/api/v4/projects/owner%2Flane" {
			t.Errorf("unexpected route %q", r.URL.EscapedPath())
		}
		w.Write([]byte(`{"name":"lane","path_with_namespace":"owner/lane","visibility":"private","web_url":"https://hub.mos.ru/owner/lane"}`))
	})
	// GitLab is hosted below /api/v4, not at server root.
	c.base += "/api/v4"
	r, err := c.Get(context.Background(), "owner", "lane")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Private || r.FullName != "owner/lane" {
		t.Fatalf("bad gitlab repo: %+v", r)
	}
}
func TestCapabilitiesDoNotClaimAPIAuthorization(t *testing.T) {
	c, err := New(GitHub)
	if err != nil {
		t.Fatal(err)
	}
	c.token = func(Site) (string, error) { return "mock", nil }
	k := c.Capability()
	if !k.CredentialPresent || strings.Contains(fmt.Sprintf("%+v", k), "mock") {
		t.Fatal("metadata leaked secret or missed credential")
	}
	if strings.Contains(strings.ToLower(k.CredentialReason), "authorized") {
		t.Fatal("credential existence must not imply authorization")
	}
}
