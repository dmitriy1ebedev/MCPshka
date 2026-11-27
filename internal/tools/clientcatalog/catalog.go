package clientcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
)

// A client may keep the tools/list it fetched when its connector was added
// (ChatGPT does) and never see tools added later. MCPshka records every
// client's last complete listing, so it can say which tools a client cannot
// see, tell the client itself (capabilities_bootstrap) and prove that a
// connector refresh worked (client_catalog_status).

type ClientCatalogEntry struct {
	Client    string            `json:"client"`
	Surface   string            `json:"surface,omitempty"`
	Version   string            `json:"version,omitempty"`
	FirstSeen time.Time         `json:"first_seen"`
	LastList  time.Time         `json:"last_list"`
	Lists     int               `json:"lists"`
	Tools     []string          `json:"tools"`
	Schemas   map[string]string `json:"schemas,omitempty"`
}

type Deps struct {
	Path       string
	Now        func() time.Time
	Hidden     func(string) bool
	CoreTools  []string
	SchemaHash func(*mcp.Tool) string
}

type Catalog struct {
	deps    Deps
	mu      sync.Mutex
	entries map[string]*ClientCatalogEntry
	// pages collects a paginated listing per session until its last page.
	pages          map[string][]string
	schemas        map[string]map[string]string
	GatewaySchemas map[string]string
}

func New(d Deps) *Catalog {
	c := &Catalog{deps: d, entries: map[string]*ClientCatalogEntry{}, pages: map[string][]string{}, schemas: map[string]map[string]string{}}
	if raw, err := os.ReadFile(d.Path); err == nil && len(raw) <= 4<<20 {
		var list []*ClientCatalogEntry
		if json.Unmarshal(raw, &list) == nil {
			for _, e := range list {
				if e != nil && e.Client != "" {
					c.entries[e.Client] = e
				}
			}
		}
	}
	return c
}

func sessionClient(s mcp.Session) (*mcp.ServerSession, string, string) {
	ss, ok := s.(*mcp.ServerSession)
	if !ok || ss == nil {
		return nil, "", ""
	}
	p := ss.InitializeParams()
	if p == nil || p.ClientInfo == nil || p.ClientInfo.Name == "" {
		return ss, "unknown", ""
	}
	name, version := p.ClientInfo.Name, p.ClientInfo.Version
	if len(name) > 128 {
		name = name[:128]
	}
	if len(version) > 64 {
		version = version[:64]
	}
	return ss, name, version
}

func (c *Catalog) Middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return c.MiddlewareFor("")(next)
}

// FullCatalogKey on a session context lists hidden tools too (catalog lints).
type FullCatalogKey struct{}

func (c *Catalog) MiddlewareFor(surface string) func(mcp.MethodHandler) mcp.MethodHandler {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if method != "tools/list" || err != nil {
				return res, err
			}
			list, ok := res.(*mcp.ListToolsResult)
			if ok && surface == "" && ctx.Value(FullCatalogKey{}) == nil && os.Getenv("MCPSHKA_TOOL_CATALOG") != "all" {
				shown := make([]*mcp.Tool, 0, len(list.Tools))
				for _, t := range list.Tools {
					if !c.deps.Hidden(t.Name) {
						shown = append(shown, t)
					}
				}
				list.Tools = shown // a new slice: the SDK may share the old backing array
			}
			ss, client, version := sessionClient(req.GetSession())
			if !ok || ss == nil {
				return res, err
			}
			cursor := ""
			if p, ok := req.GetParams().(*mcp.ListToolsParams); ok && p != nil {
				cursor = p.Cursor
			}
			names := make([]string, 0, len(list.Tools))
			schemas := map[string]string{}
			for _, t := range list.Tools {
				names = append(names, t.Name)
				schemas[t.Name] = c.deps.SchemaHash(t)
			}
			c.observeSurface(surface, ss.ID(), client, version, cursor == "", list.NextCursor == "", names, time.Now().UTC(), schemas)
			return res, err
		}
	}
}

func (c *Catalog) observeSurface(surface, session, client, version string, first, last bool, names []string, now time.Time, schemas ...map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, started := c.pages[session]; !first && !started {
		return
	}
	if first {
		c.pages[session] = nil
		c.schemas[session] = map[string]string{}
	}
	c.pages[session] = append(c.pages[session], names...)
	if len(schemas) > 0 {
		for name, hash := range schemas[0] {
			c.schemas[session][name] = hash
		}
	}
	if !last {
		return
	}
	seen := slices.Compact(slices.Sorted(slices.Values(c.pages[session])))
	delete(c.pages, session)
	e := c.entries[client]
	if e == nil {
		e = &ClientCatalogEntry{Client: client, FirstSeen: now}
		c.entries[client] = e
	}
	e.Version, e.LastList, e.Tools, e.Surface = version, now, seen, surface
	e.Schemas = c.schemas[session]
	delete(c.schemas, session)
	e.Lists++
	c.saveLocked()
}

func (c *Catalog) saveLocked() {
	if c.deps.Path == "" {
		return
	}
	list := make([]*ClientCatalogEntry, 0, len(c.entries))
	for _, e := range c.entries {
		list = append(list, e)
	}
	slices.SortFunc(list, func(a, b *ClientCatalogEntry) int { return strings.Compare(a.Client, b.Client) })
	raw, err := json.Marshal(list)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.deps.Path), ".client-catalog-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil || tmp.Close() != nil {
		tmp.Close()
		return
	}
	_ = os.Rename(tmp.Name(), c.deps.Path)
}

type ClientCatalogView struct {
	SchemaMismatch      *bool     `json:"schema_mismatch"`
	ClientRefreshNeeded bool      `json:"client_refresh_needed"`
	Client              string    `json:"client"`
	Surface             string    `json:"surface,omitempty"`
	Version             string    `json:"version,omitempty"`
	FirstSeen           time.Time `json:"first_seen"`
	LastList            time.Time `json:"last_list"`
	Lists               int       `json:"lists"`
	ToolsSeen           int       `json:"tools_seen"`
	MissingCount        int       `json:"missing_count"`
	Missing             []string  `json:"missing,omitempty"`
	Stale               bool      `json:"stale"`
	// Historical: silent for clientCatalogFresh; kept for the record, no longer drives health.
	Historical bool `json:"historical,omitempty"`
}

// clientCatalogFresh bounds how long a silent client's last listing counts
// against catalog health (MCP-181: a client gone for days kept it degraded).
const clientCatalogFresh = 72 * time.Hour

type ClientCatalogStatus struct {
	CatalogSHA256 string              `json:"catalog_sha256"`
	ServerTools   int                 `json:"server_tools"`
	Clients       []ClientCatalogView `json:"clients"`
	NextAction    string              `json:"next_action"`
}

func (c *Catalog) view(e *ClientCatalogEntry, current []string, schemas ...map[string]string) ClientCatalogView {
	if e.Surface == "gateway" {
		current = c.deps.CoreTools
		schemas = []map[string]string{c.GatewaySchemas}
	}
	v := ClientCatalogView{Surface: e.Surface, Client: e.Client, Version: e.Version, FirstSeen: e.FirstSeen, LastList: e.LastList, Lists: e.Lists, ToolsSeen: len(e.Tools)}
	for _, name := range current {
		if _, found := slices.BinarySearch(e.Tools, name); !found {
			v.MissingCount++
			if len(v.Missing) < 40 {
				v.Missing = append(v.Missing, name)
			}
		}
	}
	v.Stale = v.MissingCount > 0
	if len(schemas) > 0 && len(e.Schemas) > 0 {
		mismatch := false
		complete := true
		for _, name := range current {
			old, ok := e.Schemas[name]
			latest, present := schemas[0][name]
			if !ok || !present {
				complete = false
				continue
			}
			if old != latest {
				mismatch = true
			}
		}
		if mismatch || complete {
			v.SchemaMismatch = &mismatch
		}
		v.Stale = v.Stale || mismatch
	}
	v.Historical = !e.LastList.IsZero() && c.deps.Now().Sub(e.LastList) > clientCatalogFresh
	v.ClientRefreshNeeded = v.Stale && !v.Historical
	return v
}

func (c *Catalog) Status(current []string, schemas ...map[string]string) ClientCatalogStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := ClientCatalogStatus{ServerTools: len(current), NextAction: "every recorded client has its expected catalog"}
	for _, e := range c.entries {
		v := c.view(e, current, schemas...)
		out.Clients = append(out.Clients, v)
		if v.ClientRefreshNeeded {
			out.NextAction = "a client has missing tools or changed schemas: refresh its connector (ChatGPT: Settings → Connectors → MCPshka → refresh), meanwhile it reaches them with tool_invoke or exec_start command=mcpshka args=[\"tool-invoke\",…]"
		}
	}
	slices.SortFunc(out.Clients, func(a, b ClientCatalogView) int { return strings.Compare(a.Client, b.Client) })
	return out
}

// changedCatalogSummary reports exact new and changed names rather than only
// asking a stale MCP client to guess which tool it cannot see.
func changedCatalogSummary(e *ClientCatalogEntry, current []string, schemas ...map[string]string) string {
	var missing, changed []string
	for _, name := range current {
		if _, found := slices.BinarySearch(e.Tools, name); !found {
			missing = append(missing, name)
		} else if len(schemas) > 0 && len(e.Schemas) > 0 {
			if now, ok := schemas[0][name]; ok && e.Schemas[name] != "" && e.Schemas[name] != now {
				changed = append(changed, name)
			}
		}
	}
	parts := []string{}
	for _, group := range []struct {
		title string
		names []string
	}{{"new tools", missing}, {"changed schemas", changed}} {
		if len(group.names) > 0 {
			names := group.names
			if len(names) > 16 {
				names = names[:16]
			}
			parts = append(parts, fmt.Sprintf("%s (%d): %s", group.title, len(group.names), strings.Join(names, ", ")))
		}
	}
	return strings.Join(parts, "; ")
}

// hint is the sentence capabilities_bootstrap gives a client whose own last
// listing misses tools.
func (c *Catalog) Hint(s mcp.Session, current []string, schemas ...map[string]string) string {
	_, client, _ := sessionClient(s)
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[client]
	if e == nil {
		return ""
	}
	v := c.view(e, current, schemas...)
	if !v.Stale {
		return ""
	}
	if e.Surface == "gateway" {
		current = c.deps.CoreTools
		schemas = []map[string]string{c.GatewaySchemas}
	}
	if v.SchemaMismatch != nil && *v.SchemaMismatch {
		return fmt.Sprintf("CLIENT_SCHEMA_STALE: your client's listed schemas changed; %s. Refresh the connector or use tool_resolve with the current revision and tool_invoke", changedCatalogSummary(e, current, schemas...))
	}
	return fmt.Sprintf("your client's last tools/list (%s) lacks %d of %d tools, e.g. %s: refresh the connector, or call them via tool_invoke / exec_start command=mcpshka args=[\"tool-invoke\",\"<tool>\",\"<arguments JSON>\"]",
		e.LastList.Format(time.RFC3339), v.MissingCount, len(current), strings.Join(v.Missing[:min(5, len(v.Missing))], ", "))
}

func (c *Catalog) Health(current []string, schemas map[string]string, manifest string) healthlayer.CatalogHealth {
	h := healthlayer.CatalogHealth{CapabilityManifestHash: manifest, IndexStaleness: "unknown"}
	if c == nil {
		return h
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	known, allSchemas, mismatch, refresh := false, true, false, false
	exposed := len(current)
	missing := map[string]bool{}
	count := 0
	for _, entry := range c.entries {
		if count == 16 {
			h.Truncated = true
			break
		}
		v := c.view(entry, current, schemas)
		if v.Historical {
			continue
		}
		count++
		known = true
		exposed = min(exposed, v.ToolsSeen)
		refresh = refresh || v.Stale
		if v.SchemaMismatch == nil {
			allSchemas = false
		} else {
			mismatch = mismatch || *v.SchemaMismatch
		}
		for _, name := range v.Missing {
			if len(missing) < 8 {
				missing[name] = true
			}
		}
	}
	if known {
		h.ExposedCount = &exposed
		h.ClientRefreshNeeded = &refresh
		if allSchemas || mismatch {
			h.SchemaMismatch = &mismatch
		}
	}
	for name := range missing {
		h.MissingActions = append(h.MissingActions, name)
	}
	slices.Sort(h.MissingActions)
	return h
}

type EmptyInput = toolcontract.EmptyInput

type Snapshot struct {
	Names   []string
	Schemas map[string]string
	Digest  string
}

func Register(add func(tooldef.Def), c *Catalog, snapshot func() Snapshot) {
	closed := false
	add(tooldef.New(&mcp.Tool{
		Name:        "client_catalog_status",
		Description: "Which MCP clients (ChatGPT, Claude, Codex…) fetched tools/list, when, and which tools each client's last listing lacks. Use it to prove a connector refresh worked or to see why a chat agent cannot find a tool.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, ClientCatalogStatus, error) {
		view := snapshot()
		out := c.Status(view.Names, view.Schemas)
		out.CatalogSHA256 = view.Digest
		return nil, out, nil
	}))
}
