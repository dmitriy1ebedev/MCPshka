package ownerweb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// «Мозги»: which AI chats may connect to MCPshka as an OAuth MCP connector.
// The owner switches them on the Подключения page; a switched-off brain is
// cut off at once (registration, authorization, refresh and every bearer
// call), its tokens stay stored until expiry. A callback no brain claims is
// remembered on refusal so the owner can allow it from the page (Kimi
// publishes no callback list). Every authorization still waits for Face ID.

type oauthBrain struct {
	ID, Title string
	URIs      []string
	Match     func(uri string) bool // callbacks that cannot be listed (loopback ports)
	On        bool                  // default when the owner never switched it
}

var builtinBrains = []oauthBrain{
	{ID: "claude", Title: "Claude (claude.ai)", URIs: []string{"https://claude.ai/api/mcp/auth_callback", "https://claude.com/api/mcp/auth_callback"}, On: true},
	{ID: "chatgpt", Title: "ChatGPT", URIs: []string{"https://chatgpt.com/connector_platform_oauth_redirect"}, On: true},
	// Desktop IDEs (owner 08.11): native apps return to a loopback port chosen
	// at sign-in (RFC 8252 §7.3) or to their own scheme; Face ID still gates
	// every authorization.
	{ID: "ide", Title: "IDE на компьютере (VS Code, Cursor, Claude Code, Codex)", URIs: []string{"https://vscode.dev/redirect", "https://insiders.vscode.dev/redirect"}, Match: ideCallback, On: true},
}

// ideCallback accepts a loopback http callback on any port, or Cursor's
// own scheme; never userinfo, query or fragment.
func ideCallback(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil || len(uri) > 256 || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "http":
		host := u.Hostname()
		return (host == "127.0.0.1" || host == "localhost" || host == "::1") && u.Port() != ""
	case "cursor":
		return strings.HasPrefix(u.Host, "anysphere.")
	}
	return false
}

type brainExtra struct {
	URI    string    `json:"uri"`
	Client string    `json:"client,omitempty"`
	On     bool      `json:"on"`
	SeenAt time.Time `json:"seen_at"`
}

type brainState struct {
	Enabled map[string]bool `json:"enabled,omitempty"`
	Extra   []brainExtra    `json:"extra,omitempty"`
}

const maxBrainExtra = 8

var (
	brainMu   sync.Mutex
	brainFile = func() string { return connPath("oauth-brains.json") }
)

func loadBrains() brainState {
	var st brainState
	if raw, err := os.ReadFile(brainFile()); err == nil && len(raw) <= 65536 {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func saveBrains(st brainState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := brainFile() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, brainFile())
}

func (st brainState) builtinOn(b oauthBrain) bool {
	if on, ok := st.Enabled[b.ID]; ok {
		return on
	}
	return b.On
}

// redirectAllowed: the callback belongs to a switched-on brain.
func redirectAllowed(uri string) bool {
	brainMu.Lock()
	defer brainMu.Unlock()
	st := loadBrains()
	for _, b := range builtinBrains {
		for _, u := range b.URIs {
			if u == uri {
				return st.builtinOn(b)
			}
		}
		if b.Match != nil && b.Match(uri) {
			return st.builtinOn(b)
		}
	}
	for _, e := range st.Extra {
		if e.URI == uri {
			return e.On
		}
	}
	return false
}

func clientAllowed(c oauthClient) bool {
	for _, u := range c.RedirectURIs {
		if !redirectAllowed(u) {
			return false
		}
	}
	return len(c.RedirectURIs) > 0
}

// validCallback: only a bare https callback is ever offered to the owner.
func validCallback(uri string) bool {
	u, err := url.Parse(uri)
	return err == nil && len(uri) <= 256 && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// noteRejected remembers a refused callback for the page, newest first.
func noteRejected(uri, client string, now time.Time) {
	if !validCallback(uri) {
		return
	}
	brainMu.Lock()
	defer brainMu.Unlock()
	st := loadBrains()
	for i, e := range st.Extra {
		if e.URI == uri {
			st.Extra[i].SeenAt, st.Extra[i].Client = now, client
			_ = saveBrains(st)
			return
		}
	}
	st.Extra = append(st.Extra, brainExtra{URI: uri, Client: client, SeenAt: now})
	sort.SliceStable(st.Extra, func(i, j int) bool {
		if st.Extra[i].On != st.Extra[j].On {
			return st.Extra[i].On // allowed ones are never pushed out
		}
		return st.Extra[i].SeenAt.After(st.Extra[j].SeenAt)
	})
	if len(st.Extra) > maxBrainExtra {
		st.Extra = st.Extra[:maxBrainExtra]
	}
	_ = saveBrains(st)
}

func brainExtraID(uri string) string {
	sum := sha256.Sum256([]byte(uri))
	return "brain-" + hex.EncodeToString(sum[:6])
}

// setBrain switches a builtin brain or a remembered callback.
func setBrain(id string, on bool) error {
	brainMu.Lock()
	defer brainMu.Unlock()
	st := loadBrains()
	for _, b := range builtinBrains {
		if "brain-"+b.ID == id {
			if st.Enabled == nil {
				st.Enabled = map[string]bool{}
			}
			st.Enabled[b.ID] = on
			return saveBrains(st)
		}
	}
	for i, e := range st.Extra {
		if brainExtraID(e.URI) == id {
			st.Extra[i].On = on
			return saveBrains(st)
		}
	}
	return errors.New("неизвестный ИИ-клиент")
}

// brainStatuses are the «Мозги» rows of the Подключения page.
func brainStatuses() []ConnStatus {
	brainMu.Lock()
	defer brainMu.Unlock()
	st := loadBrains()
	const group = "Мозги: ИИ-чаты с доступом к MCPshka"
	state := func(on bool) string {
		if on {
			return "connected"
		}
		return "off"
	}
	var out []ConnStatus
	for _, b := range builtinBrains {
		out = append(out, ConnStatus{ID: "brain-" + b.ID, Group: group, Title: b.Title, Kind: "brain", State: state(st.builtinOn(b)),
			Why: "Коннектор MCP по OAuth; каждое подключение — с Face ID. Выключено — доступ отрезан сразу.", Redirect: strings.Join(b.URIs, ", ")})
	}
	for _, e := range st.Extra {
		title := "Новый ИИ-клиент"
		if e.Client != "" {
			title = e.Client
		}
		s := "ask"
		if e.On {
			s = "connected"
		}
		out = append(out, ConnStatus{ID: brainExtraID(e.URI), Group: group, Title: title, Kind: "brain", State: s, Redirect: e.URI,
			Why: "Пытался подключиться " + e.SeenAt.Local().Format("02.01 15:04") + " с этим адресом возврата. Разрешите, если это ваш ИИ (например Kimi)."})
	}
	return out
}
