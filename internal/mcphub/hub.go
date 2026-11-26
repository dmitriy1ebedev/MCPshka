// Package mcphub makes MCPshka the one plugin a chat needs (MCP-187, owner
// 14.11.2026: «хочу просто писать задачи и контролировать результат»):
// MCPshka is an MCP client of other remote MCP servers the owner connected on
// the «Плагины» page. Their tools are found by tool_resolve and run by
// tool_invoke as `<server>.<tool>`, so tools/list does not grow; every call
// is counted and logged with redacted arguments, and nothing the hub holds
// as a secret can leave in a call.
//
// Layout under Dir: servers.json (what is connected), secrets/<id>.json
// (0400, never returned), tools/<id>.json (cached catalog), calls.jsonl
// (ledger).
package mcphub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Auth kinds of a server.
const (
	AuthNone  = "none"
	AuthToken = "token"
	AuthOAuth = "oauth"
)

// Server is one connected MCP server. It never holds a secret.
type Server struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	URL      string    `json:"url"`
	Auth     string    `json:"auth"`
	Source   string    `json:"source,omitempty"` // registry name or "manual"
	Added    time.Time `json:"added"`
	Disabled bool      `json:"disabled,omitempty"`
}

// Secret is what a server's sign-in left; it stays in secrets/<id>.json.
type Secret struct {
	Token        string    `json:"token,omitempty"`
	Refresh      string    `json:"refresh,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	TokenURL     string    `json:"token_url,omitempty"`
	Resource     string    `json:"resource,omitempty"`
}

var (
	ErrNotFound = errors.New("mcphub: server not connected")
	idRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// Hub is the store plus the client side. The zero value is not usable: use New.
type Hub struct {
	Dir string
	Now func() time.Time

	mu    sync.Mutex // servers.json and secrets
	calls sync.Mutex // calls.jsonl
	sem   chan struct{}
}

// DefaultDir is the production state directory.
const DefaultDir = "/var/lib/mcpshka/mcphub"

// MaxParallel bounds outgoing calls (H96 has ~3.7 GB RAM).
const MaxParallel = 4

func New(dir string) *Hub {
	return &Hub{Dir: dir, Now: time.Now, sem: make(chan struct{}, MaxParallel)}
}

// ValidURL accepts only plain HTTPS endpoints: no credentials in the URL, no
// fragment. Private and loopback targets are refused at dial time.
func ValidURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || len(raw) > 512 {
		return nil, errors.New("нужен адрес https:// без логина и пароля в нём")
	}
	return u, nil
}

// Slug makes a server id from a title or host.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := b.String()
	if len(out) > 32 {
		out = out[:32]
	}
	return strings.Trim(out, "-")
}

func (h *Hub) path(parts ...string) string {
	return filepath.Join(append([]string{h.Dir}, parts...)...)
}

// Servers lists connected servers, sorted by title.
func (h *Hub) Servers() ([]Server, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.load()
}

func (h *Hub) load() ([]Server, error) {
	raw, err := os.ReadFile(h.path("servers.json"))
	if errors.Is(err, os.ErrNotExist) {
		return []Server{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Server
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcphub: servers.json: %w", err)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Title < out[b].Title })
	return out, nil
}

func (h *Hub) save(list []Server) error {
	raw, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(h.path("servers.json"), raw, 0o600)
}

// Server returns one connected server.
func (h *Hub) Server(id string) (Server, error) {
	list, err := h.Servers()
	if err != nil {
		return Server{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return Server{}, ErrNotFound
}

// Put adds or replaces a server; the id is derived when empty and made unique.
func (h *Hub) Put(s Server) (Server, error) {
	u, err := ValidURL(s.URL)
	if err != nil {
		return s, err
	}
	s.URL = u.String()
	if s.Auth == "" {
		s.Auth = AuthNone
	}
	if s.Auth != AuthNone && s.Auth != AuthToken && s.Auth != AuthOAuth {
		return s, errors.New("mcphub: auth must be none, token or oauth")
	}
	s.Title = strings.TrimSpace(s.Title)
	if s.Title == "" {
		s.Title = u.Hostname()
	}
	if len(s.Title) > 80 {
		s.Title = s.Title[:80]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	list, err := h.load()
	if err != nil {
		return s, err
	}
	if s.ID == "" {
		base := Slug(s.Title)
		if base == "" {
			base = Slug(u.Hostname())
		}
		s.ID = base
		for n := 2; taken(list, s.ID); n++ {
			s.ID = fmt.Sprintf("%.28s-%d", base, n)
		}
	}
	if !idRe.MatchString(s.ID) {
		return s, errors.New("mcphub: invalid server id")
	}
	if s.Added.IsZero() {
		s.Added = h.Now().UTC()
	}
	kept := list[:0]
	for _, x := range list {
		if x.ID != s.ID {
			kept = append(kept, x)
		}
	}
	return s, h.save(append(kept, s))
}

func taken(list []Server, id string) bool {
	for _, x := range list {
		if x.ID == id {
			return true
		}
	}
	return false
}

// Remove forgets a server, its secret and its cached tools; the ledger stays.
func (h *Hub) Remove(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	list, err := h.load()
	if err != nil {
		return err
	}
	kept, found := list[:0], false
	for _, x := range list {
		if x.ID == id {
			found = true
			continue
		}
		kept = append(kept, x)
	}
	if !found {
		return ErrNotFound
	}
	if err := h.save(kept); err != nil {
		return err
	}
	_ = os.Remove(h.path("secrets", id+".json"))
	_ = os.Remove(h.path("tools", id+".json"))
	return nil
}

// SetDisabled pauses or resumes a server without forgetting its sign-in.
func (h *Hub) SetDisabled(id string, off bool) error {
	s, err := h.Server(id)
	if err != nil {
		return err
	}
	s.Disabled = off
	_, err = h.Put(s)
	return err
}

// PutSecret stores a server's sign-in, readable by the service only.
func (h *Hub) PutSecret(id string, sec Secret) error {
	if !idRe.MatchString(id) {
		return errors.New("mcphub: invalid server id")
	}
	raw, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return writeAtomic(h.path("secrets", id+".json"), raw, 0o400)
}

func (h *Hub) secret(id string) (Secret, bool) {
	raw, err := os.ReadFile(h.path("secrets", id+".json"))
	if err != nil {
		return Secret{}, false
	}
	var s Secret
	if json.Unmarshal(raw, &s) != nil {
		return Secret{}, false
	}
	return s, s.Token != "" || s.Refresh != ""
}

// SecretInfo is what the page may show about a sign-in.
type SecretInfo struct {
	Present bool      `json:"present"`
	Last4   string    `json:"last4,omitempty"`
	Expiry  time.Time `json:"expiry,omitempty"`
	Refresh bool      `json:"refresh,omitempty"`
}

func (h *Hub) SecretInfo(id string) SecretInfo {
	s, ok := h.secret(id)
	if !ok {
		return SecretInfo{}
	}
	info := SecretInfo{Present: true, Expiry: s.Expiry, Refresh: s.Refresh != ""}
	if len(s.Token) >= 12 {
		info.Last4 = "…" + s.Token[len(s.Token)-4:]
	}
	return info
}

// secrets returns every stored secret value, for the outbound guard.
func (h *Hub) secrets() []string {
	files, _ := filepath.Glob(h.path("secrets", "*.json"))
	var out []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s Secret
		if json.Unmarshal(raw, &s) == nil {
			out = append(out, s.Token, s.Refresh, s.ClientSecret)
		}
	}
	return out
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hub-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
