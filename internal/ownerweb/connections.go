package ownerweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/lanelink"
	"mcpshka/internal/nodes"
	"mcpshka/internal/storagefabric"
)

// «Подключения» is the owner's settings page on the public HTTPS origin: one
// Face ID (root broker scope connections_portal, a one-shot grant bound to a
// browser cookie) opens a 30-minute session from any browser, the iPhone
// included. Every external connection MCPshka uses is listed with its
// observed state and where to get it. Tokens are write-only: checked at the
// provider first, then stored atomically 0400 for the service; the page never
// returns a secret, only account, rights, expiry and the last four characters.
// Clouds reuse the storage OAuth redirect flow (callback on this origin, not
// localhost) and Codex signs in by device code, so no step needs a browser on
// H96 itself.

const (
	connSessionTTL   = 30 * time.Minute
	connLoginTTL     = 5 * time.Minute
	connCookie       = "mcpshka_conn"
	connLoginCookie  = "mcpshka_conn_login"
	connMaxBody      = 16384
	connStorageOwner = "connections-portal"
)

// ConnCheck is what a provider confirmed about a token. Never a secret.
type ConnCheck struct {
	OK      bool   `json:"ok"`
	Account string `json:"account,omitempty"`
	Scopes  string `json:"scopes,omitempty"`
	Expires string `json:"expires,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type connProvider struct {
	ID, Group, Title, Kind string // kind: token, cloud, agent, form
	Why, Hint, HintURL     string
	Path                   string
	check                  func(ctx context.Context, c *http.Client, token string) ConnCheck
}

// ConnStatus is one row of the page.
type ConnStatus struct {
	ID       string           `json:"id"`
	Group    string           `json:"group"`
	Title    string           `json:"title"`
	Kind     string           `json:"kind"`
	Why      string           `json:"why"`
	Hint     string           `json:"hint,omitempty"`
	HintURL  string           `json:"hint_url,omitempty"`
	State    string           `json:"state"`
	Last4    string           `json:"last4,omitempty"`
	Updated  string           `json:"updated,omitempty"`
	Redirect string           `json:"redirect_uri,omitempty"`
	App      bool             `json:"app,omitempty"`
	Quick    bool             `json:"quick,omitempty"`
	Fields   []ConnFieldView  `json:"fields,omitempty"`
	Check    *ConnCheck       `json:"check,omitempty"`
	Lane     *lanelink.Status `json:"lane,omitempty"`
	Node     *nodes.Node      `json:"node,omitempty"`
}

var connStateDir = "/var/lib/mcpshka"

// Lane MCP port (docs/LANE_MCP_PORT.md): Lane on this host publishes the
// local MCP endpoint; MCPshka's own OAuth/bearer still guards it.
var (
	laneBase     = lanelink.DefaultBase
	laneLocalURL = "http://127.0.0.1:8765"
	// Card states: online is connected; pending waits for the code in Lane.
	laneCardState = map[string]string{"online": "connected", "pending": "pending", "unpaired": "missing", "no_lane": "missing", "offline": "offline", "paused": "paused", "error": "invalid"}
)

func (p *connPortal) lane() *lanelink.Link {
	return &lanelink.Link{Base: laneBase, HTTP: p.http, State: connPath("lane/link.json")}
}

func connPath(rel string) string { return filepath.Join(connStateDir, rel) }

type connPortal struct {
	mu       sync.Mutex
	sessions map[string]connSession // sha256(cookie) → session
	logins   map[string]connLogin   // grant id → pending login
	checks   map[string]ConnCheck   // last observed check per provider
	http     *http.Client
	now      func() time.Time
	broker   func(ctx context.Context, family string, args ...string) (json.RawMessage, error)
	storage  *storageConnectState
}

var connWeb = &connPortal{
	sessions: map[string]connSession{},
	logins:   map[string]connLogin{},
	checks:   map[string]ConnCheck{},
	http:     &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	now:      time.Now,
	broker: func(ctx context.Context, family string, args ...string) (json.RawMessage, error) {
		raw, err := brokerclient.Run(ctx, family, args...)
		if err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, errors.New("invalid broker response")
		}
		return raw, nil
	},
	storage: storageWeb,
}

func (p *connPortal) cloudStates(ctx context.Context) map[string]string {
	out := map[string]string{}
	raw, err := json.Marshal(storagefabric.Request{Action: "providers", Owner: connStorageOwner})
	if err != nil {
		return out
	}
	res, err := p.broker(ctx, "storage", string(raw))
	if err != nil {
		return out
	}
	var r storagefabric.Result
	if json.Unmarshal(res, &r) != nil {
		return out
	}
	for _, pr := range r.Providers {
		out[pr.ID] = pr.State + "/" + pr.Authenticated
	}
	return out
}

// statuses observes every row; token rows show their last check, if any.
func (p *connPortal) statuses(ctx context.Context) []ConnStatus {
	apps, _ := LoadStorageApps()
	clouds := p.cloudStates(ctx)
	redirect, _ := storageRedirectURI()
	var out []ConnStatus
	for _, c := range connCatalog {
		s := ConnStatus{ID: c.ID, Group: c.Group, Title: c.Title, Kind: c.Kind, Why: c.Why, Hint: c.Hint, HintURL: c.HintURL}
		switch c.Kind {
		case "token":
			if t, mod, ok := connReadToken(c.Path); ok {
				s.State, s.Last4, s.Updated = "saved", last4(t), mod.UTC().Format("2006-01-02")
				p.mu.Lock()
				if ch, ok := p.checks[c.ID]; ok {
					chc := ch
					s.Check = &chc
					s.State = map[bool]string{true: "connected", false: "invalid"}[ch.OK]
				}
				p.mu.Unlock()
			} else {
				s.State = "missing"
			}
		case "cloud":
			s.Quick = storageQuick[c.ID]
			s.Hint = "«Подключить» откроет обычный вход в сервис. После входа браузер покажет пустую страницу 127.0.0.1 — скопируйте её адрес и вставьте сюда."
			if !s.Quick {
				s.Hint = "«Подключить» откроет мастер: один раз завести своё приложение в облаке, дальше вход в одно нажатие."
			}
			if _, ok := apps[c.ID]; ok {
				s.App = true
			}
			if c.ID != "onedrive" {
				s.Redirect = redirect
			}
			if strings.HasPrefix(clouds[c.ID], "credential_present/") {
				s.State = "connected"
			} else {
				s.State = "missing"
			}
		case "agent":
			// The broker's `codex login status` line decides (owner 03.11.2026:
			// «Logged in using ChatGPT», but the card still said «проверьте вход»).
			s.State = "unknown"
			if line := p.agentStatus(ctx, c.ID); line != "" {
				s.Check = &ConnCheck{OK: strings.HasPrefix(line, "Logged in"), Detail: line}
				s.State = map[bool]string{true: "connected", false: "missing"}[s.Check.OK]
			}
		case "form":
			v, mod := readForm(c.ID)
			s.Fields = formView(c.ID, v)
			if v == nil {
				s.State = "missing"
				break
			}
			s.State, s.Updated = "saved", mod.UTC().Format("2006-01-02")
			p.mu.Lock()
			if ch, ok := p.checks[c.ID]; ok {
				chc := ch
				s.Check = &chc
				s.State = map[bool]string{true: "connected", false: "invalid"}[ch.OK]
			}
			p.mu.Unlock()
		case "lane":
			st, err := p.lane().Status(ctx)
			if err != nil {
				st = lanelink.Status{State: "error", Detail: err.Error()}
			}
			s.Lane = &st
			s.State = laneCardState[st.State]
		default:
			s.State = "planned"
		}
		out = append(out, s)
	}
	out = append(out, brainStatuses()...)
	out = append(out, nodeStatuses()...)
	return out
}

func (p *connPortal) check(ctx context.Context, c connProvider, token string) ConnCheck {
	if c.check == nil {
		return ConnCheck{Detail: "проверка не поддерживается"}
	}
	ch := c.check(ctx, p.http, token)
	if len(ch.Detail) > 300 {
		ch.Detail = ch.Detail[:300]
	}
	p.mu.Lock()
	p.checks[c.ID] = ch
	p.mu.Unlock()
	return ch
}

// page serves the single-page UI with a per-response script nonce.
func (p *connPortal) page(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nonce := RandomHex(16)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, strings.Replace(connectionsHTML, "<script>", "<script nonce=\""+nonce+"\">", 1))
}

func (p *connPortal) route(w http.ResponseWriter, r *http.Request) {
	if !portalGuard(w, r) {
		return
	}
	switch path := r.URL.Path; {
	case path == "/connections" || path == "/connections/":
		p.page(w, r)
	case path == "/connections/containers":
		p.containersPage(w, r)
	case path == "/connections/resources":
		p.resourcesPage(w, r)
	case path == "/connections/code":
		p.codePage(w, r)
	case path == "/connections/ide":
		p.idePage(w, r)
	case path == "/connections/runners":
		p.runnersPage(w, r)
	case path == "/connections/live":
		p.livePage(w, r)
	case path == "/connections/issues":
		if IssuesPage == nil {
			http.NotFound(w, r)
			return
		}
		IssuesPage(w, r)
	case path == "/connections/memory":
		memoryPage(w, r)
	case path == "/connections/plugins":
		p.pluginsPage(w, r)
	case path == pluginCallback:
		pluginCallbackRoute(w, r)
	case path == "/connections/login" && r.Method == http.MethodPost:
		p.login(w, r)
	case path == "/connections/session" && r.Method == http.MethodPost:
		p.session(w, r)
	case path == "/connections/logout" && r.Method == http.MethodPost:
		p.logout(w, r)
	case strings.HasPrefix(path, "/connections/api/"):
		p.api(w, r)
	default:
		http.NotFound(w, r)
	}
}

func registerConnectionsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/connections", connWeb.route)
	mux.HandleFunc("/connections/", connWeb.route)
}

// connGroups keeps the catalog order of groups for the page.
func connGroups() []string {
	seen := map[string]int{}
	var out []string
	for i, c := range connCatalog {
		if _, ok := seen[c.Group]; !ok {
			seen[c.Group] = i
			out = append(out, c.Group)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return seen[out[a]] < seen[out[b]] })
	return out
}

// agentStatus returns the first `codex login status` line via the broker
// (bounded; empty when unavailable).
func (p *connPortal) agentStatus(ctx context.Context, id string) string {
	if id != "codex" || p.broker == nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := p.broker(cctx, "agent-login", "codex-status")
	if err != nil {
		return ""
	}
	var r struct {
		Message, State, Status string
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	for _, v := range []string{r.Message, r.Status, r.State} {
		if strings.HasPrefix(v, "Logged in") || strings.HasPrefix(v, "Not logged in") {
			return v
		}
	}
	return ""
}
