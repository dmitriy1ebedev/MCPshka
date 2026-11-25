package mcphub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Entry is one server offered on the «Магазин» tab.
type Entry struct {
	Name        string `json:"name"`  // registry name, e.g. io.github.upstash/context7
	Title       string `json:"title"` // display name
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
	Repo        string `json:"repo,omitempty"`
	// Verified: the vendor's own domain answered as an MCP endpoint when the
	// list was written; the page probes again before connecting.
	Verified bool   `json:"verified,omitempty"`
	Auth     string `json:"auth,omitempty"` // expected sign-in: none, oauth, token; "" = unknown until probed
}

// Known are official remote endpoints checked 14.11.2026; the open registry
// also lists forks under similar names, so these come first and are marked.
var Known = []Entry{
	{Name: "context7", Title: "Context7", Description: "Свежая документация библиотек и примеры кода", URL: "https://mcp.context7.com/mcp", Auth: AuthNone},
	{Name: "supabase", Title: "Supabase", Description: "Проекты, базы данных и функции Supabase", URL: "https://mcp.supabase.com/mcp", Auth: AuthOAuth},
	{Name: "vercel", Title: "Vercel", Description: "Проекты, деплои и логи Vercel", URL: "https://mcp.vercel.com", Auth: AuthOAuth},
	{Name: "stackoverflow", Title: "Stack Overflow", Description: "Вопросы и ответы Stack Overflow", URL: "https://mcp.stackoverflow.com", Auth: AuthOAuth},
	{Name: "postman", Title: "Postman", Description: "Коллекции, окружения и API Postman", URL: "https://mcp.postman.com/mcp", Auth: AuthOAuth},
	{Name: "parallel", Title: "Parallel", Description: "Веб-поиск и исследования Parallel", URL: "https://search-mcp.parallel.ai/mcp", Auth: AuthOAuth},
	{Name: "gitbook", Title: "GitBook", Description: "Документация и пространства GitBook", URL: "https://mcp.gitbook.com/mcp", Auth: AuthOAuth},
	{Name: "dropbox", Title: "Dropbox", Description: "Файлы и поиск Dropbox", URL: "https://mcp.dropbox.com/mcp", Auth: AuthOAuth},
	{Name: "alpaca", Title: "Alpaca", Description: "Рыночные данные и брокерский счёт Alpaca", URL: "https://mcp.alpaca.markets/mcp", Auth: AuthToken},
	{Name: "github", Title: "GitHub", Description: "Репозитории, задачи и PR GitHub", URL: "https://api.githubcopilot.com/mcp/", Auth: AuthToken},
	{Name: "gitverse", Title: "GitVerse", Description: "Репозитории и CI GitVerse", URL: "https://mcp.gitverse.ru", Auth: AuthToken},
}

func init() {
	for i := range Known {
		Known[i].Verified = true
	}
}

// RegistryURL is the open MCP Registry.
var RegistryURL = "https://registry.modelcontextprotocol.io/v0/servers"

const catalogTTL = 12 * time.Hour

// Search returns curated matches first, then registry servers with a
// streamable-http remote. The registry answer is cached per query.
func (h *Hub) Search(ctx context.Context, q string) ([]Entry, error) {
	q = strings.TrimSpace(strings.ToLower(q))
	var out []Entry
	for _, e := range Known {
		if q == "" || strings.Contains(strings.ToLower(e.Title+" "+e.Name+" "+e.Description), q) {
			out = append(out, e)
		}
	}
	// Open servers first (owner: «в идеале подключать открытые»), sign-in later.
	sort.SliceStable(out, func(a, b int) bool { return out[a].Auth == AuthNone && out[b].Auth != AuthNone })
	if q == "" {
		return out, nil
	}
	reg, err := h.registry(ctx, q)
	seen := map[string]bool{}
	for _, e := range out {
		seen[e.URL] = true
	}
	sort.SliceStable(reg, func(a, b int) bool { return reg[a].Auth == "" && reg[b].Auth != "" })
	for _, e := range reg {
		if !seen[e.URL] {
			seen[e.URL] = true
			out = append(out, e)
		}
	}
	return out, err
}

func (h *Hub) registry(ctx context.Context, q string) ([]Entry, error) {
	key := Slug(q)
	if key == "" {
		return nil, nil
	}
	cache := h.path("catalog", key+".json")
	if st, err := os.Stat(cache); err == nil && h.Now().Sub(st.ModTime()) < catalogTTL {
		if raw, err := os.ReadFile(cache); err == nil {
			var out []Entry
			if json.Unmarshal(raw, &out) == nil {
				return out, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var page struct {
		Servers []struct {
			Server struct {
				Name        string `json:"name"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Repository  struct {
					URL string `json:"url"`
				} `json:"repository"`
				Remotes []struct {
					Type    string `json:"type"`
					URL     string `json:"url"`
					Headers []struct {
						Name string `json:"name"`
					} `json:"headers"`
				} `json:"remotes"`
			} `json:"server"`
		} `json:"servers"`
	}
	c := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	if err := getJSON(ctx, c, RegistryURL+"?"+url.Values{"search": {q}, "limit": {"30"}}.Encode(), &page); err != nil {
		return nil, err
	}
	var out []Entry
	for _, s := range page.Servers {
		for _, r := range s.Server.Remotes {
			if r.Type != "streamable-http" || !strings.HasPrefix(r.URL, "https://") || strings.Contains(r.URL, "{") {
				continue
			}
			auth := ""
			for _, hd := range r.Headers {
				if strings.EqualFold(hd.Name, "Authorization") {
					auth = AuthToken
				}
			}
			title := s.Server.Title
			if title == "" {
				title = s.Server.Name
			}
			out = append(out, Entry{Name: s.Server.Name, Title: clip(title, 80), Description: clip(s.Server.Description, 200), URL: r.URL, Repo: s.Server.Repository.URL, Auth: auth})
			break
		}
	}
	raw, _ := json.Marshal(out)
	_ = writeAtomic(cache, raw, 0o600)
	return out, nil
}

// Seen is one plugin a chat client reported (plugin_sync) or the host found
// in Claude Code's configuration. Names and public URLs only.
type Seen struct {
	Client string    `json:"client"` // chatgpt, claude, claude-code
	Name   string    `json:"name"`
	URL    string    `json:"url,omitempty"`
	At     time.Time `json:"at"`
}

// Verdict groups a seen plugin for the page.
const (
	Builtin     = "builtin"     // MCPshka already does this itself
	Connected   = "connected"   // already in the hub
	Connectable = "connectable" // a remote MCP endpoint is known
	OwnLogin    = "own_login"   // needs a separate sign-in MCPshka cannot inherit
	ClientOnly  = "client_only" // exists only inside the chat client
	Unknown     = "unknown"     // nothing matched yet: search the store
)

// Match is a seen plugin with what to do about it.
type Match struct {
	Seen
	Verdict string `json:"verdict"`
	Note    string `json:"note,omitempty"`
	Entry   *Entry `json:"entry,omitempty"`
	Server  string `json:"server,omitempty"`
}

// builtins are client plugins whose job MCPshka already does natively.
var builtins = map[string]string{
	"mcpshka": "это сама MCPshka", "github": "Git-площадки MCPshka (git_provider_*, forge_ci)",
	"google drive": "облака MCPshka (storage_*)", "dropbox": "облака MCPshka (storage_*)",
	"gateway": "исполнитель MCPshka на H96 (exec_start, project_run)", "dns inspector": "сеть и Lane (network_*)",
	"automations": "миссии и напоминания MCPshka (mission_*, memory_write)",
}

var clientOnly = map[string]string{
	"plugin creator": "служебный плагин ChatGPT", "plugin management": "служебный плагин ChatGPT",
	"claude code remote": "служебный коннектор Claude",
}

var ownLogin = map[string]string{
	"gmail": "нужен свой вход Google для MCPshka", "google calendar": "нужен свой вход Google для MCPshka",
	"icloud calendar": "пароль приложения Apple (CalDAV)", "openai platform": "ключ API OpenAI",
	"remote desktop commander": "агент на вашем компьютере", "slidesgpt": "только в Claude", "claude docs": "документы Claude",
}

// Sync stores what a client reported (replacing that client's previous list)
// and returns every seen plugin with its verdict.
func (h *Hub) Sync(client string, seen []Seen) ([]Match, error) {
	client = Slug(client)
	if client == "" {
		client = "chat"
	}
	all := h.seen()
	kept := all[:0]
	for _, s := range all {
		if s.Client != client {
			kept = append(kept, s)
		}
	}
	now := h.Now().UTC()
	for i, s := range seen {
		if i >= 200 {
			break
		}
		s.Client, s.Name, s.At = client, clip(strings.TrimSpace(s.Name), 80), now
		if s.URL != "" {
			if _, err := ValidURL(s.URL); err != nil {
				s.URL = ""
			}
		}
		if s.Name != "" {
			kept = append(kept, s)
		}
	}
	raw, _ := json.Marshal(kept)
	if err := writeAtomic(h.path("seen.json"), raw, 0o600); err != nil {
		return nil, err
	}
	return h.Matches()
}

func (h *Hub) seen() []Seen {
	raw, err := os.ReadFile(h.path("seen.json"))
	if err != nil {
		return nil
	}
	var out []Seen
	_ = json.Unmarshal(raw, &out)
	return out
}

// Matches is every seen plugin with its verdict, one row per name.
func (h *Hub) Matches() ([]Match, error) {
	servers, err := h.Servers()
	if err != nil {
		return nil, err
	}
	byName := map[string]*Match{}
	var order []string
	for _, s := range h.seen() {
		key := strings.ToLower(s.Name)
		if m, ok := byName[key]; ok {
			if !strings.Contains(m.Client, s.Client) {
				m.Client += ", " + s.Client
			}
			continue
		}
		m := &Match{Seen: s}
		byName[key] = m
		order = append(order, key)
		h.judge(m, servers)
	}
	sort.SliceStable(order, func(a, b int) bool { return rank(byName[order[a]].Verdict) < rank(byName[order[b]].Verdict) })
	out := make([]Match, 0, len(order))
	for _, k := range order {
		out = append(out, *byName[k])
	}
	return out, nil
}

func (h *Hub) judge(m *Match, servers []Server) {
	key := strings.ToLower(m.Name)
	for _, s := range servers {
		if strings.EqualFold(s.Title, m.Name) || m.URL != "" && s.URL == m.URL || strings.Contains(strings.ToLower(s.Source), key) {
			m.Verdict, m.Server = Connected, s.ID
			return
		}
	}
	if n, ok := builtins[key]; ok {
		m.Verdict, m.Note = Builtin, n
		return
	}
	if n, ok := clientOnly[key]; ok {
		m.Verdict, m.Note = ClientOnly, n
		return
	}
	for i, e := range Known {
		if strings.EqualFold(e.Title, m.Name) || strings.EqualFold(e.Name, Slug(m.Name)) || m.URL != "" && e.URL == m.URL {
			m.Verdict, m.Entry = Connectable, &Known[i]
			return
		}
	}
	if m.URL != "" {
		m.Verdict, m.Entry = Connectable, &Entry{Name: Slug(m.Name), Title: m.Name, URL: m.URL}
		return
	}
	if n, ok := ownLogin[key]; ok {
		m.Verdict, m.Note = OwnLogin, n
		return
	}
	m.Verdict, m.Note = Unknown, "поищите в магазине"
}

func rank(v string) int {
	return map[string]int{Connectable: 0, Unknown: 1, OwnLogin: 2, Connected: 3, Builtin: 4, ClientOnly: 5}[v]
}
