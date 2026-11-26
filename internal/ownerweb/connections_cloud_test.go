package ownerweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRcloneTokenParsesJSONAndBase64(t *testing.T) {
	tok := `{"access_token":"A","token_type":"Bearer","refresh_token":"R","expiry":"2026-11-03T12:00:00Z"}`
	for _, out := range []string{
		"Paste the following into your remote machine --->\n" + tok + "\n<---End paste\n",
		"--->\n" + base64.StdEncoding.EncodeToString([]byte(`{"token":`+strconvQuote(tok)+`}`)) + "\n<---End paste",
	} {
		got, err := rcloneToken(out)
		if err != nil || got != tok {
			t.Fatalf("%q → %q %v", out, got, err)
		}
	}
	for _, bad := range []string{"", "--->{}<---End paste", "---> !!! <---End paste"} {
		if _, err := rcloneToken(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// fakeRclone imitates rclone authorize: prints the local link, serves /auth
// with a redirect to the provider, then prints the token after the callback.
func fakeRclone(t *testing.T) *cloudFlows {
	t.Helper()
	tok := `{"access_token":"ACCESS","token_type":"Bearer","refresh_token":"R"}`
	got := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth" && r.URL.Query().Get("state") == "st8":
			http.Redirect(w, r, "https://accounts.example/o/oauth2/auth?state=st8", http.StatusTemporaryRedirect)
		case r.URL.Path == "/" && r.URL.Query().Get("code") == "C0DE" && r.URL.Query().Get("state") == "st8":
			got <- struct{}{}
		default:
			http.Error(w, "bad", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return &cloudFlows{
		local: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		base:  srv.URL,
		start: func(ctx context.Context, backend, dir string) (*exec.Cmd, io.ReadCloser, *bytes.Buffer, error) {
			if backend != "drive" {
				t.Fatalf("backend %s", backend)
			}
			pr, pw := io.Pipe()
			out := &bytes.Buffer{}
			cmd := exec.CommandContext(ctx, "sleep", "30")
			if err := cmd.Start(); err != nil {
				return nil, nil, nil, err
			}
			go func() {
				io.WriteString(pw, "NOTICE: go to http://127.0.0.1:53682/auth?state=st8\n")
				pw.Close()
				select {
				case <-got:
					out.WriteString("--->\n" + tok + "\n<---End paste\n")
					cmd.Process.Kill()
				case <-ctx.Done():
				}
			}()
			return cmd, pr, out, nil
		},
	}
}

func TestCloudFlowSignsInWithRclone(t *testing.T) {
	f := fakeRclone(t)
	link, err := f.begin("drive")
	if err != nil || link != "https://accounts.example/o/oauth2/auth?state=st8" {
		t.Fatalf("begin %q %v", link, err)
	}
	if _, err := f.finish("drive", "http://127.0.0.1:53682/?state=st8"); err == nil {
		t.Fatal("accepted a landing address without code")
	}
	if _, err := f.finish("yandex", "http://127.0.0.1:53682/?code=C0DE&state=st8"); err == nil {
		t.Fatal("finished another provider's flow")
	}
	tok, err := f.finish("drive", " http://127.0.0.1:53682/?state=st8&code=C0DE&scope=x ")
	if err != nil || !strings.Contains(tok, `"access_token":"ACCESS"`) {
		t.Fatalf("finish %q %v", tok, err)
	}
	if _, err := f.finish("drive", "http://127.0.0.1:53682/?state=st8&code=C0DE"); err == nil {
		t.Fatal("flow reusable after success")
	}
}

func TestOneDriveDriveAddsDriveID(t *testing.T) {
	c := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://graph.microsoft.com/v1.0/me/drive" || r.Header.Get("Authorization") != "Bearer ACCESS" {
			t.Fatalf("request %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"b!xyz","driveType":"personal"}`)), Header: http.Header{}}, nil
	})}
	got, err := oneDriveDrive(context.Background(), c, `{"access_token":"ACCESS","token_type":"Bearer"}`)
	if err != nil || !strings.Contains(got, `"drive_id":"b!xyz"`) || !strings.Contains(got, `"drive_type":"personal"`) {
		t.Fatalf("%s %v", got, err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConnectionFormsValidateCheckAndKeepSecrets(t *testing.T) {
	p, _ := newConnTestPortal(t)
	session := connLoginFlow(t, p)
	var auth string
	p.http = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		code := 200
		if auth != "Bearer cf-token-123" {
			code = 401
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(`{"result":{"status":"active"}}`)), Header: http.Header{}}, nil
	})}
	post := func(path, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		p.route(rr, connReq(http.MethodPost, path, body, session))
		return rr
	}
	if rr := post("/connections/api/form", `{"id":"cloudflare","fields":{"api_token":"wrong-token-1"}}`); rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("rejected token saved: %d", rr.Code)
	}
	if rr := post("/connections/api/form", `{"id":"cloudflare","fields":{"api_token":"cf-token-123","x":"1"}}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("extra field: %d", rr.Code)
	}
	if rr := post("/connections/api/form", `{"id":"cloudflare","fields":{"api_token":"cf-token-123"}}`); rr.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rr.Code, rr.Body)
	}
	if rr := post("/connections/api/form", `{"id":"cloudflare","fields":{"api_token":""}}`); rr.Code != http.StatusOK || auth != "Bearer cf-token-123" {
		t.Fatalf("empty secret must keep the stored one: %d %s", rr.Code, auth)
	}
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodGet, "/connections/api/catalog", "", session))
	if strings.Contains(rr.Body.String(), "cf-token-123") || !strings.Contains(rr.Body.String(), `"last4":"…-123"`) {
		t.Fatalf("catalog leaks or hides the secret: %s", rr.Body)
	}
	if _, err := formValues("s3", map[string]string{"bucket": "my-backups", "access_key": "YCAJE0123456", "secret_key": "secret-secret"}); err != nil {
		t.Fatalf("s3 defaults: %v", err)
	}
	if _, err := formValues("ntfy", map[string]string{"server": "http://plain.example", "topic": "h96"}); err == nil {
		t.Fatal("ntfy accepted plain http")
	}
}

func TestS3RequestIsSigned(t *testing.T) {
	v := map[string]string{"endpoint": "https://storage.yandexcloud.net", "region": "ru-central1", "bucket": "b-1", "access_key": "AKID1234", "secret_key": "s3cr3t"}
	req, err := s3ListRequest(context.Background(), v, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	a := req.Header.Get("Authorization")
	if req.URL.String() != "https://storage.yandexcloud.net/b-1/?list-type=2&max-keys=1" || !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=AKID1234/20261103/ru-central1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=") || len(a) < 200 {
		t.Fatalf("%s %s", req.URL, a)
	}
}

func TestMosHubSecretPushUsesSavedTokens(t *testing.T) {
	p, _ := newConnTestPortal(t)
	session := connLoginFlow(t, p)
	if err := connWriteToken("moshub/api_token", "glpat-moshub-0001"); err != nil {
		t.Fatal(err)
	}
	if err := connWriteToken("gitverse/api_token", "gv-token-0002"); err != nil {
		t.Fatal(err)
	}
	var body, method, auth string
	p.http = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		body, method, auth = string(raw), r.Method, r.Header.Get("Authorization")
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	rr := httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/ci-secret", `{"id":"moshub"}`, session))
	if rr.Code != http.StatusOK || method != http.MethodPut || auth != "Bearer gv-token-0002" || body != `{"data":"glpat-moshub-0001"}` {
		t.Fatalf("%d %s %s %s", rr.Code, method, auth, body)
	}
	rr = httptest.NewRecorder()
	p.route(rr, connReq(http.MethodPost, "/connections/api/ci-secret", `{"id":"github"}`, session))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("github ci-secret: %d", rr.Code)
	}
}

func TestLandedQueryAcceptsPhonePastes(t *testing.T) {
	for _, in := range []string{
		"http://127.0.0.1:53682/?state=st8&code=4/0AbC_d-e&scope=https://www.googleapis.com/auth/drive",
		"127.0.0.1:53682/?state=st8&code=4/0AbC_d-e&scope=https://www.googleapis.com/auth/drive",
		" localhost:53682/?code=4%2F0AbC_d-e&state=st8 ",
		"?state=st8&code=4/0AbC_d-e",
		"http%3A%2F%2F127.0.0.1%3A53682%2F%3Fstate%3Dst8%26code%3D4%2F0AbC_d-e",
	} {
		q, err := landedQuery(in)
		if err != nil || q.Get("state") != "st8" || (q.Get("code") != "4/0AbC_d-e" && q.Get("code") != "4%2F0AbC_d-e") {
			t.Fatalf("%q → %v %v", in, q, err)
		}
	}
	for _, bad := range []string{"", "127.0.0.1:53682", "https://accounts.google.com/o/oauth2/auth?state=st8"} {
		if _, err := landedQuery(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestCodexCardFollowsLoginStatus(t *testing.T) {
	p, _ := newConnTestPortal(t)
	base := p.broker
	p.broker = func(ctx context.Context, family string, args ...string) (json.RawMessage, error) {
		if family == "agent-login" {
			return json.RawMessage(`{"state":"logged_in","message":"Logged in using ChatGPT"}`), nil
		}
		return base(ctx, family, args...)
	}
	for _, c := range p.statuses(context.Background()) {
		if c.ID == "codex" && (c.State != "connected" || c.Check == nil || c.Check.Detail != "Logged in using ChatGPT") {
			t.Fatalf("codex card %+v", c)
		}
	}
}
