package ownerweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

const (
	portalTestOrigin = "https://h96-host.example.test"
	portalTestID     = "0123456789abcdef0123456789abcdef"
)

func portalTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", portalTestOrigin)
}

func portalTestRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, portalTestOrigin+path, strings.NewReader(body))
	if method == http.MethodPost {
		r.Header.Set("Origin", portalTestOrigin)
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func portalTestServe(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	PasskeyPortal(rec, r)
	return rec
}

func TestPortalOriginConfiguredRejectsLooseOrigins(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
		ok    bool
	}{
		{"https://x.test", "https://x.test", true},
		{"https://x.test/", "https://x.test", true},
		{"  https://x.test  ", "https://x.test", true},
		{"https://x.test:8443", "https://x.test:8443", true},
		{"", "", false},
		{"http://x.test", "", false},
		{"ftp://x.test", "", false},
		{"https://user:pw@x.test", "", false},
		{"https://x.test/approval", "", false},
		{"https://x.test?x=1", "", false},
		{"https://x.test#frag", "", false},
		{"https:///only-a-path", "", false},
		{"not a url", "", false},
	} {
		t.Setenv("MCPSHKA_PASSKEY_ORIGIN", tc.value)
		got, err := OriginConfigured()
		if tc.ok != (err == nil) || got != tc.want {
			t.Fatalf("%q: got %q, %v; want %q ok=%v", tc.value, got, err, tc.want, tc.ok)
		}
	}
}

func TestPortalGuardChecksHostOriginAndSetsHardeningHeaders(t *testing.T) {
	t.Setenv("MCPSHKA_PASSKEY_ORIGIN", "")
	if rec := portalTestServe(portalTestRequest(http.MethodGet, "/approval/"+portalTestID, "")); rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured portal must not exist: %d", rec.Code)
	}

	portalTestEnv(t)
	wrongHost := httptest.NewRequest(http.MethodGet, "https://evil.example.test/approval/"+portalTestID, nil)
	if rec := portalTestServe(wrongHost); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Host must be refused: %d", rec.Code)
	}

	wrongOrigin := portalTestRequest(http.MethodPost, "/passkey/enroll/begin", `{"code":"123456"}`)
	wrongOrigin.Header.Set("Origin", "https://evil.example.test")
	if rec := portalTestServe(wrongOrigin); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Origin on POST must be refused: %d", rec.Code)
	}
	noOrigin := portalTestRequest(http.MethodPost, "/passkey/enroll/begin", `{"code":"123456"}`)
	noOrigin.Header.Del("Origin")
	if rec := portalTestServe(noOrigin); rec.Code != http.StatusForbidden {
		t.Fatalf("POST without Origin must be refused: %d", rec.Code)
	}

	rec := portalTestServe(portalTestRequest(http.MethodGet, "/approval/resolve/zz", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid prefix must stop before the broker: %d", rec.Code)
	}
	for k, want := range map[string]string{
		"Cache-Control":          "no-store, max-age=0",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("default CSP must forbid scripts and framing: %q", csp)
	}

	if rec := portalTestServe(portalTestRequest(http.MethodPut, "/approval/"+portalTestID, "")); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT must be refused: %d", rec.Code)
	}
	if rec := portalTestServe(portalTestRequest(http.MethodDelete, "/approval/"+portalTestID, "")); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE must be refused: %d", rec.Code)
	}
}

func TestPortalPageGetsAFreshScriptNonce(t *testing.T) {
	portalTestEnv(t)
	nonceRE := regexp.MustCompile(`script-src 'nonce-([A-Za-z0-9+/]+)'`)
	seen := map[string]bool{}
	for _, path := range []string{"/approval/" + portalTestID, "/approval/" + portalTestID, "/passkey/enroll"} {
		rec := portalTestServe(portalTestRequest(http.MethodGet, path, ""))
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		csp := rec.Header().Get("Content-Security-Policy")
		m := nonceRE.FindStringSubmatch(csp)
		if m == nil || strings.Contains(csp, "script-src 'unsafe-inline'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Fatalf("%s: weak CSP %q", path, csp)
		}
		nonce := m[1]
		if len(nonce) < 24 || seen[nonce] {
			t.Fatalf("%s: nonce must be fresh and at least 24 characters: %q", path, nonce)
		}
		seen[nonce] = true
		body := rec.Body.String()
		if !strings.Contains(body, `<script nonce="`+nonce+`">`) || strings.Contains(body, "<script>") {
			t.Fatalf("%s: the single inline script must carry the nonce", path)
		}
	}
	if strings.Count(PasskeyHTML, "<script>") != 1 {
		t.Fatal("the page must keep exactly one inline script, otherwise a second one runs without a nonce")
	}
}

func TestPortalRoutesRefuseBadShapesBeforeTheBroker(t *testing.T) {
	portalTestEnv(t)
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"prefix with uppercase", http.MethodGet, "/approval/resolve/ABCDEF12"},
		{"prefix too short", http.MethodGet, "/approval/resolve/abc"},
		{"traversal in prefix", http.MethodGet, "/approval/resolve/../x"},
		{"id too short", http.MethodGet, "/approval/0123abcd/options"},
		{"id of 31 characters", http.MethodGet, "/approval/" + portalTestID[:31] + "/options"},
		{"id with uppercase", http.MethodGet, "/approval/" + strings.ToUpper(portalTestID) + "/options"},
		{"id with non-hex character", http.MethodGet, "/approval/" + portalTestID[:31] + "g/options"},
		{"unknown action", http.MethodPost, "/approval/" + portalTestID + "/unknown"},
		{"unknown path", http.MethodPost, "/nonexistent"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if rec := portalTestServe(portalTestRequest(tc.method, tc.path, "{}")); rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s: %d", tc.method, tc.path, rec.Code)
			}
		})
	}
}

func TestPortalBodyErrorsAreRefusedBeforeTheBroker(t *testing.T) {
	portalTestEnv(t)
	for _, tc := range []struct {
		name string
		path string
		body string
		want string
	}{
		{"code of five digits", "/passkey/enroll/begin", `{"code":"12345"}`, "six digits required"},
		{"malformed JSON", "/passkey/enroll/begin", `{`, "malformed JSON"},
		{"no code and no credential", "/passkey/enroll/begin", `{}`, "missing credential"},
		{"finish without credential", "/approval/" + portalTestID + "/finish", `{}`, "missing credential"},
		{"credential above 12000 bytes", "/approval/" + portalTestID + "/finish", `{"credential":"` + strings.Repeat("a", 12001) + `"}`, "missing credential"},
		{"body above 16 KiB", "/approval/" + portalTestID + "/finish", `{"x":"` + strings.Repeat("a", 17000) + `"}`, "request too large"},
		{"enroll finish without credential", "/passkey/enroll/finish", `{}`, "missing credential"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := portalTestServe(portalTestRequest(http.MethodPost, tc.path, tc.body))
			var out struct {
				Error string `json:"error"`
			}
			if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/json" || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Error != tc.want {
				t.Fatalf("%s: %d %q %q", tc.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
			}
		})
	}
}

func TestPortalBodyPrefersCodeOverCredential(t *testing.T) {
	read := func(body string) (string, error) {
		return portalBody(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)))
	}
	if got, err := read(`{"code":"123456","credential":{"a":1}}`); err != nil || got != "123456" {
		t.Fatalf("code wins: %q %v", got, err)
	}
	if got, err := read(`{"credential":{"a":1}}`); err != nil || got != `{"a":1}` {
		t.Fatalf("credential is passed through verbatim: %q %v", got, err)
	}
	for _, bad := range []string{"", "[]", "not json"} {
		if _, err := read(bad); err == nil || err.Error() != "malformed JSON" {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

// MCP-243: after navigator.credentials.get the finish request may already be
// committed on the server. The page must remember that, reconcile with GET
// only, and never re-enable the button for a second Face ID.
func TestPortalFaceIDFinishIsNeverReplayedAfterAssertion(t *testing.T) {
	html := PasskeyHTML
	get := strings.Index(html, "navigator.credentials.get(getOptions(opts))")
	sent := strings.Index(html, "finishSent=true;\n      rememberPending();")
	finish := strings.Index(html, "request('/approval/'+id+'/finish'")
	if get < 0 || sent < 0 || finish < 0 || !(get < sent && sent < finish) {
		t.Fatalf("finishSent must be set after the assertion and before /finish: %d %d %d", get, sent, finish)
	}
	if strings.Count(html, "finishSent=true;\n      rememberPending();") != 1 || strings.Count(html, "request('/approval/'+id+'/finish'") != 1 {
		t.Fatal("exactly one place may send the assertion")
	}
	guard := strings.LastIndex(html, "if(finishSent){")
	reenable := regexp.MustCompile(`show\(e\);\s*btn\.disabled=false;`).FindStringIndex(html)
	if guard < 0 || reenable == nil || guard > reenable[0] {
		t.Fatal("after a possible commit the error path must not re-enable the Face ID button")
	}
	for _, want := range []string{
		"window.addEventListener('pageshow',()=>{if(finishSent)void reconcileAfterFinish();});",
		"window.addEventListener('online',()=>{if(finishSent)void reconcileAfterFinish();});",
		"document.addEventListener('visibilitychange',()=>{if(!document.hidden&&finishSent)void reconcileAfterFinish();});",
		"pendingFromPreviousPage()",
		"rememberPending();",
		"clearPending();",
		"recoveryInFlight=true;",
		"attempt<8",
		"await reconcileAfterFinish()",
		"Date.now()>Date.parse(exp)-15000",
		"fresh.state==='authorized'||fresh.state==='active'",
		"Face ID выполнен, но ответ сервера не подтверждён",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("recovery lost %q", want)
		}
	}
	if strings.Count(html, "location.assign('/oauth/approved/'+id)") < 2 {
		t.Fatal("both the finish and the reconcile path must continue the connector OAuth flow")
	}
}
