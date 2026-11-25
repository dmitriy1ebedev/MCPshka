package lanelink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeLane implements docs/LANE_MCP_PORT.md: pairing is unauthenticated,
// port operations need the port's own bearer key.
type fakeLane struct {
	secretSum, token, state string
	approved, revoked       bool
}

func (f *fakeLane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		return
	}
	var in struct {
		Operation string         `json:"operation"`
		Params    map[string]any `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	reply := func(result map[string]any) {
		result["ok"] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "lane.api/v1", "ok": true, "result": result})
	}
	authed := !f.revoked && r.Header.Get("Authorization") == "Bearer "+f.token
	switch in.Operation {
	case "mcpport.pair":
		f.secretSum = in.Params["secret_sha256"].(string)
		reply(map[string]any{"request_id": "req-1", "code": "482913", "expires_at": "2099-01-01T00:00:00Z"})
	case "mcpport.pair_status":
		sum := sha256.Sum256([]byte(in.Params["secret"].(string)))
		if hex.EncodeToString(sum[:]) != f.secretSum {
			http.Error(w, "bad secret", http.StatusForbidden)
			return
		}
		if !f.approved {
			reply(map[string]any{"state": "pending"})
			return
		}
		f.token = "tok-1"
		reply(map[string]any{"state": "approved", "port_id": "p1", "token": f.token, "public_url": "https://mcp.example/p1"})
	case "mcpport.status":
		if !authed {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply(map[string]any{"state": f.state, "public_url": "https://mcp.example/p1", "route_label": "через ваш сервер в NL", "bytes_in": 10})
	case "mcpport.rotate":
		if !authed {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.token = "tok-2"
		reply(map[string]any{"token": f.token})
	case "mcpport.remove":
		if !authed {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reply(map[string]any{})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"message": "unknown operation"}})
	}
}

func newLink(t *testing.T, h http.Handler) *Link {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Link{Base: srv.URL, HTTP: srv.Client(), State: filepath.Join(t.TempDir(), "lanelink.json")}
}

func TestPairConfirmStatusRotateRemove(t *testing.T) {
	ctx := context.Background()
	f := &fakeLane{state: "online"}
	l := newLink(t, f)
	if s, _ := l.Status(ctx); s.State != "unpaired" {
		t.Fatalf("before pairing: %+v", s)
	}
	s, err := l.Pair(ctx, "MCPshka H96", "http://127.0.0.1:8765")
	if err != nil || s.State != "pending" || s.Code != "482913" {
		t.Fatalf("pair: %+v %v", s, err)
	}
	if s, _ := l.Status(ctx); s.State != "pending" || s.Code != "482913" {
		t.Fatalf("not yet confirmed: %+v", s)
	}
	f.approved = true
	s, err = l.Status(ctx)
	if err != nil || s.State != "online" || s.PublicURL != "https://mcp.example/p1" || s.Route == "" {
		t.Fatalf("confirmed: %+v %v", s, err)
	}
	if fi, _ := os.Stat(l.State); fi.Mode().Perm() != 0o600 {
		t.Fatalf("state mode %v", fi.Mode())
	}
	raw, _ := os.ReadFile(l.State)
	var kept saved
	_ = json.Unmarshal(raw, &kept)
	if kept.Secret != "" || kept.Token != "tok-1" {
		t.Fatalf("pairing secret must be dropped once the key arrives: %+v", kept)
	}
	if err := l.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := l.Status(ctx); s.State != "online" {
		t.Fatalf("after rotate: %+v", s)
	}
	if err := l.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.State); !os.IsNotExist(err) {
		t.Fatal("remove must drop the local key")
	}
}

func TestRevokedKeyAsksToReconnect(t *testing.T) {
	ctx := context.Background()
	f := &fakeLane{state: "online", approved: true}
	l := newLink(t, f)
	if _, err := l.Pair(ctx, "x", "http://127.0.0.1:8765"); err != nil {
		t.Fatal(err)
	}
	if s, _ := l.Status(ctx); s.State != "online" {
		t.Fatalf("%+v", s)
	}
	f.revoked = true
	if s, _ := l.Status(ctx); s.State != "error" || s.Detail != ErrRevoked.Error() {
		t.Fatalf("revoked: %+v", s)
	}
	if err := l.Remove(ctx); err != nil {
		t.Fatalf("removing a revoked port must still clean up: %v", err)
	}
}

func TestNoLane(t *testing.T) {
	l := &Link{Base: "http://127.0.0.1:1", HTTP: http.DefaultClient, State: filepath.Join(t.TempDir(), "s.json")}
	if s, _ := l.Status(context.Background()); s.State != "no_lane" {
		t.Fatalf("%+v", s)
	}
	if s, _ := l.Pair(context.Background(), "x", "y"); s.State != "no_lane" {
		t.Fatalf("%+v", s)
	}
}

func TestLaneRefusalIsNamed(t *testing.T) {
	l := newLink(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"ok": false, "error": "pairing disabled by policy"}})
	}))
	if _, err := l.Pair(context.Background(), "x", "y"); err == nil || err.Error() != "Lane mcpport.pair: pairing disabled by policy" {
		t.Fatalf("%v", err)
	}
}
