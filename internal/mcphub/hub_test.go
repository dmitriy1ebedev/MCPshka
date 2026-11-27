package mcphub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer is a real MCP server over streamable HTTP with one read-only
// and one writing tool; it records the Authorization header it saw.
func fakeServer(t *testing.T, wantToken string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	type echoIn struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo text", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_all", Description: "Deletes things"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deleted"}}}, nil, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.Header().Set("WWW-Authenticate", `Bearer realm="fake"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	old := transport
	transport = ts.Client().Transport // the TLS test server is on loopback
	t.Cleanup(func() { transport = old })
	return ts, &seen
}

func newHub(t *testing.T) *Hub {
	h := New(t.TempDir())
	return h
}

func TestOpenServerToolsCallsAndLedger(t *testing.T) {
	ts, _ := fakeServer(t, "")
	h := newHub(t)
	if p := h.Probe(context.Background(), ts.URL); p.Auth != AuthNone || p.Tools != 2 {
		t.Fatal("probe", p)
	}
	s, err := h.Put(Server{Title: "Fake Docs", URL: ts.URL})
	if err != nil || s.ID != "fake-docs" {
		t.Fatal(s, err)
	}
	tools, err := h.Tools(context.Background(), s.ID, true)
	if err != nil || len(tools) != 2 {
		t.Fatal(tools, err)
	}
	res, err := h.Call(context.Background(), CallInput{Server: s.ID, Tool: "echo", Args: map[string]any{"text": "private words"}, Caller: "chatgpt"})
	if err != nil || res.Status != "OK" || !strings.Contains(res.Text, "echo: private words") || res.Trust != "UNTRUSTED" {
		t.Fatal(res, err)
	}
	// a writing tool needs the owner's window
	if res, err := h.Call(context.Background(), CallInput{Server: s.ID, Tool: "delete_all", Args: map[string]any{}}); err == nil || res.Status != "APPROVAL_REQUIRED" {
		t.Fatal("unapproved write ran", res, err)
	}
	if res, err := h.Call(context.Background(), CallInput{Server: s.ID, Tool: "delete_all", Args: map[string]any{}, Approved: true}); err != nil || res.Status != "OK" {
		t.Fatal(res, err)
	}
	calls := h.Calls("", 10)
	if len(calls) != 3 || calls[0].Status != "OK" || calls[1].Status != "APPROVAL_REQUIRED" || calls[2].Args != "{text:string(15)}" {
		t.Fatal(calls)
	}
	raw, _ := os.ReadFile(filepath.Join(h.Dir, "calls.jsonl"))
	if strings.Contains(string(raw), "private words") {
		t.Fatal("argument value reached the ledger")
	}
	st := h.Stats()[s.ID]
	if st == nil || st.Calls != 3 || st.Errors != 1 || len(st.Tools) != 2 {
		t.Fatal(st)
	}
}

func TestTokenServerSecretNeverLeaves(t *testing.T) {
	const token = "tok-0123456789abcdef-secret"
	ts, seen := fakeServer(t, token)
	h := newHub(t)
	if p := h.Probe(context.Background(), ts.URL); p.Auth != AuthToken {
		t.Fatal("probe", p)
	}
	s, _ := h.Put(Server{Title: "Locked", URL: ts.URL, Auth: AuthToken})
	if _, err := h.Call(context.Background(), CallInput{Server: s.ID, Tool: "echo", Args: map[string]any{"text": "x"}}); err == nil || !strings.Contains(err.Error(), "LOGIN_NEEDED") {
		t.Fatal("call without sign-in", err)
	}
	if err := h.PutSecret(s.ID, Secret{Token: token}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(h.Dir, "secrets", s.ID+".json")); err != nil || st.Mode().Perm() != 0o400 {
		t.Fatal("secret mode", st, err)
	}
	if info := h.SecretInfo(s.ID); !info.Present || info.Last4 != "…cret" {
		t.Fatal(info)
	}
	if res, err := h.Call(context.Background(), CallInput{Server: s.ID, Tool: "echo", Args: map[string]any{"text": "hi"}}); err != nil || res.Status != "OK" {
		t.Fatal(res, err)
	}
	if (*seen)[len(*seen)-1] != "Bearer "+token {
		t.Fatal("token not sent to its own server")
	}
	// the token itself, or any secret shape, never goes out as an argument
	for _, bad := range []map[string]any{
		{"text": "see " + token},
		{"text": "-----BEGIN OPENSSH PRIVATE KEY-----\nabc"},
		{"text": "ghp_" + strings.Repeat("a", 36)},
		{"text": "https://user:pa55word@example.com/x"},
		{"note": map[string]any{"cfg": "password=hunter2hunter2"}},
	} {
		n := len(*seen)
		res, err := h.Call(context.Background(), CallInput{Server: s.ID, Tool: "echo", Args: bad, Known: []string{"host-secret-value-1"}})
		if !errors.Is(err, ErrSecretBlocked) && !strings.Contains(err.Error(), "SECRET_BLOCKED") || res.Status != "SECRET_BLOCKED" || len(*seen) != n {
			t.Fatal("secret left", bad, res, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(h.Dir, "calls.jsonl"))
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "hunter2") {
		t.Fatal("secret in ledger")
	}
	if st := h.Stats()[s.ID]; st.Blocked != 5 {
		t.Fatal(st)
	}
}

func TestGuardHostSecretsAndRedact(t *testing.T) {
	if err := Guard(map[string]any{"q": "how to sort a slice"}, []string{"host-secret-value-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Guard(map[string]any{"q": "x host-secret-value-1"}, []string{"host-secret-value-1"}); !errors.Is(err, ErrSecretBlocked) || strings.Contains(err.Error(), "host-secret") {
		t.Fatal(err)
	}
	for _, fine := range []string{"sketchbook drawings and skateboarding-club-2026", "token bucket rate limiter", "AKIA is a prefix"} {
		if err := Guard(map[string]any{"q": fine}, nil); err != nil {
			t.Fatal("false positive", fine, err)
		}
	}
	if err := Guard(map[string]any{"cfg": map[string]any{"password": "hunter2hunter2"}}, nil); !errors.Is(err, ErrSecretBlocked) {
		t.Fatal("json password left", err)
	}
	if got := Redact(map[string]any{"b": true, "a": "xyz", "n": 3.0}); got != "{a:string(5) b:bool(4) n:number(1)}" {
		t.Fatal(got)
	}
}

func TestPublicOnly(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "192.168.77.10", "10.1.2.3", "100.108.44.203", "169.254.1.1", "::1", "fd00::1", "0.0.0.0"} {
		if Public(net.ParseIP(ip)) {
			t.Fatal("private allowed", ip)
		}
	}
	if !Public(net.ParseIP("1.1.1.1")) {
		t.Fatal("public refused")
	}
	if publicOnly("tcp", "192.168.0.1:443", nil) == nil {
		t.Fatal("dial guard")
	}
	for _, bad := range []string{"http://x.example/mcp", "https://u:p@x.example", "https://x.example/#f", "ftp://x"} {
		if _, err := ValidURL(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestSyncVerdicts(t *testing.T) {
	ts, _ := fakeServer(t, "")
	h := newHub(t)
	h.Now = func() time.Time { return time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC) }
	if _, err := h.Put(Server{Title: "Postman", URL: ts.URL}); err != nil {
		t.Fatal(err)
	}
	m, err := h.Sync("ChatGPT", []Seen{{Name: "GitHub"}, {Name: "Context7"}, {Name: "Gmail"}, {Name: "Plugin Creator"}, {Name: "Postman"}, {Name: "Something New"}, {Name: "Own", URL: "https://own.example/mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range m {
		got[x.Name] = x.Verdict
	}
	want := map[string]string{"GitHub": Builtin, "Context7": Connectable, "Gmail": OwnLogin, "Plugin Creator": ClientOnly, "Postman": Connected, "Something New": Unknown, "Own": Connectable}
	for k, v := range want {
		if got[k] != v {
			t.Fatal(k, got[k], "want", v)
		}
	}
	// a second client adds itself to the same row; a re-sync replaces its list
	m, _ = h.Sync("claude", []Seen{{Name: "Gmail"}})
	m, _ = h.Sync("chatgpt", []Seen{{Name: "Gmail"}})
	if len(m) != 1 || m[0].Client != "claude, chatgpt" {
		t.Fatal(m)
	}
}
