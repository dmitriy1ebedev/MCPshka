package ownerweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The owner pages («Код», «Ресурсы») call the same MCPshka tools as ChatGPT,
// through the full tool wrapper (schema, receipts, probes), as the identity
// "owner-web" whose scopes are exactly WebTools — the authorization layer, not
// this file, refuses anything else. Lease tokens for «Код» stay server-side in
// the Face ID session; the browser never sees them.

// ToolCall is set by mcpserver: it invokes one registered tool as owner-web.
var ToolCall func(ctx context.Context, name string, args map[string]any) (json.RawMessage, error)

// WebTools is the complete list the owner pages may call.
var WebTools = []string{
	// «Код»
	"fs_list", "fs_read", "fs_find", "fs_stat", "fs_write", "git_status", "git_diff",
	"project_context", "project_commit", "project_worktree", "project_test", "project_lease_status", "job_status", "job_output",
	// «Ресурсы»
	"health_snapshot", "host_triage", "host_storage_inventory", "project_cleanup_candidates", "tool_usage",
}

// leaseTools take the session's lease token for the project containing path.
var leaseTools = map[string]bool{"fs_write": true, "project_commit": true, "project_test": true}

const webLeaseOwner = "owner-web"

type webLeases struct {
	mu     sync.Mutex
	tokens map[string]map[string]string // session hash → project root → token
}

var codeLeases = &webLeases{tokens: map[string]map[string]string{}}

func (l *webLeases) token(session, path string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	best := ""
	for root := range l.tokens[session] {
		if (path == root || strings.HasPrefix(path, root+"/")) && len(root) > len(best) {
			best = root
		}
	}
	if best == "" {
		return ""
	}
	return l.tokens[session][best]
}

func (l *webLeases) put(session, root, token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tokens[session] == nil {
		l.tokens[session] = map[string]string{}
	}
	l.tokens[session][root] = token
}

func (l *webLeases) take(session, root string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	tok := l.tokens[session][root]
	delete(l.tokens[session], root)
	return tok
}

// drop returns and forgets every lease of a session (logout or expiry).
func (l *webLeases) drop(session string) map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.tokens[session]
	delete(l.tokens, session)
	return out
}

func releaseLeases(tokens map[string]string) {
	if ToolCall == nil {
		return
	}
	for root, tok := range tokens {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = ToolCall(ctx, "project_lease_release", map[string]any{"path": root, "token": tok, "idempotency_key": "owner-web-release-" + RandomHex(8)})
		}()
	}
}

func (p *connPortal) sessionKey(r *http.Request) string {
	c, err := r.Cookie(connCookie)
	if err != nil {
		return ""
	}
	return sum256(c.Value)
}

type toolRequest struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
	Path string         `json:"path"`
}

// toolsAPI serves tool (generic), code-open, code-close and code-heartbeat.
func (p *connPortal) toolsAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if ToolCall == nil {
		jsonErr(w, "инструменты недоступны", http.StatusServiceUnavailable)
		return
	}
	var in toolRequest
	if err := ReadBody(r, &in); err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	session := p.sessionKey(r)
	switch action {
	case "tool":
		if !slices.Contains(WebTools, in.Tool) {
			jsonErr(w, "инструмент недоступен с веб-страницы: "+in.Tool, http.StatusBadRequest)
			return
		}
		args := in.Args
		if args == nil {
			args = map[string]any{}
		}
		delete(args, "lease_token") // only the session's own lease is ever used
		if leaseTools[in.Tool] {
			if path, _ := args["path"].(string); path != "" {
				if tok := codeLeases.token(session, filepath.Clean(path)); tok != "" {
					args["lease_token"] = tok
				}
			}
		}
		p.toolReply(w, ctx, in.Tool, args)
	case "code-open":
		root := filepath.Clean(in.Path)
		raw, err := ToolCall(ctx, "project_lease_acquire", map[string]any{"path": root, "owner": webLeaseOwner, "idempotency_key": "owner-web-acquire-" + RandomHex(8)})
		if err != nil {
			jsonErr(w, err.Error(), http.StatusConflict)
			return
		}
		var res struct {
			Status struct {
				ProjectRoot string `json:"project_root"`
			} `json:"status"`
			Token string `json:"token"`
		}
		if json.Unmarshal(raw, &res) != nil || res.Token == "" || res.Status.ProjectRoot == "" {
			jsonErr(w, "проект не захвачен: нет токена", http.StatusConflict)
			return
		}
		codeLeases.put(session, res.Status.ProjectRoot, res.Token)
		JSON(w, map[string]any{"root": res.Status.ProjectRoot, "held": true}, http.StatusOK)
	case "code-close", "code-heartbeat":
		root := filepath.Clean(in.Path)
		tok := codeLeases.token(session, root)
		if tok == "" {
			JSON(w, map[string]any{"held": false}, http.StatusOK)
			return
		}
		if action == "code-close" {
			codeLeases.take(session, root)
			releaseLeases(map[string]string{root: tok})
			JSON(w, map[string]any{"held": false}, http.StatusOK)
			return
		}
		if _, err := ToolCall(ctx, "project_lease_heartbeat", map[string]any{"path": root, "token": tok, "idempotency_key": "owner-web-hb-" + RandomHex(8)}); err != nil {
			jsonErr(w, err.Error(), http.StatusConflict)
			return
		}
		JSON(w, map[string]any{"held": true}, http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (p *connPortal) toolReply(w http.ResponseWriter, ctx context.Context, name string, args map[string]any) {
	cctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	raw, err := ToolCall(cctx, name, args)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		JSON(w, map[string]string{"error": err.Error()}, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}
