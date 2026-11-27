package fstools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/tooldef"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

type SearchStartInput struct {
	Path           string `json:"path"`
	Pattern        string `json:"pattern"`
	Glob           string `json:"glob,omitempty"`
	Regex          bool   `json:"regex,omitempty"`
	MaxResults     int    `json:"max_results,omitempty"`
	TimeoutMS      int    `json:"timeout_ms,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}
type SearchMoreInput struct {
	ID     string `json:"id"`
	Cursor string `json:"cursor,omitempty"`
}
type searchSession struct {
	owner string
	q     filesystem.FindQuery
	last  time.Time
}
type searchSessions struct {
	sync.Mutex
	items map[string]searchSession
}

const searchIdleTTL = 10 * time.Minute

type SearchListResult struct {
	Sessions []string `json:"sessions"`
}

var searchOwner = authn.Owner

func (s *searchSessions) start(owner string, q filesystem.FindQuery) (string, error) {
	s.Lock()
	defer s.Unlock()
	for id, v := range s.items {
		if time.Since(v.last) > searchIdleTTL {
			delete(s.items, id)
		}
	}
	if len(s.items) >= 32 {
		return "", errors.New("search session limit reached (32); stop a session")
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	s.items[id] = searchSession{owner: owner, q: q, last: time.Now()}
	return id, nil
}
func (s *searchSessions) get(id, owner string, stop bool) (filesystem.FindQuery, error) {
	s.Lock()
	defer s.Unlock()
	v, ok := s.items[id]
	if !ok || v.owner != owner || time.Since(v.last) > searchIdleTTL {
		return filesystem.FindQuery{}, errors.New("search session not found or expired")
	}
	if stop {
		delete(s.items, id)
	} else {
		v.last = time.Now()
		s.items[id] = v
	}
	return v.q, nil
}
func (s *searchSessions) list(owner string) SearchListResult {
	s.Lock()
	defer s.Unlock()
	ids := []string{}
	for id, session := range s.items {
		if session.owner == owner && time.Since(session.last) <= searchIdleTTL {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return SearchListResult{Sessions: ids}
}

func registerSearch(add func(tooldef.Def), d Deps) {
	sessions := &searchSessions{items: map[string]searchSession{}}
	add(tooldef.New(&mcp.Tool{Name: "search_start", Description: "Create a content search session over an allowed path using the fs_find engine, RE2 or literal pattern and optional basename/relative-path glob. No index duplication. Up to 32 sessions, 10 minute idle TTL; fetch results with search_more.", Annotations: &mcp.ToolAnnotations{}}, func(ctx context.Context, req *mcp.CallToolRequest, in SearchStartInput) (*mcp.CallToolResult, map[string]any, error) {
		if in.Pattern == "" || len(in.Pattern) > 4096 {
			return nil, nil, errors.New("pattern must hold 1..4096 bytes")
		}
		if in.Regex {
			if _, err := regexp.Compile(in.Pattern); err != nil {
				return nil, nil, err
			}
		}
		if _, err := filepath.Match(in.Glob, ""); err != nil {
			return nil, nil, err
		}
		root, err := d.Manager.Policy().ResolveExisting(in.Path)
		if err != nil {
			return nil, nil, err
		}
		n := in.MaxResults
		if n <= 0 {
			n = 50
		}
		q := filesystem.FindQuery{Path: root, Pattern: in.Pattern, Regex: in.Regex, Glob: in.Glob, MaxResults: min(n, 50), Timeout: time.Duration(max(1, min(in.TimeoutMS, 5000))) * time.Millisecond}
		if in.TimeoutMS <= 0 {
			q.Timeout = 5 * time.Second
		}
		id, err := sessions.start(searchOwner(ctx), q)
		return nil, map[string]any{"id": id, "cursor": "", "idle_ttl_seconds": 600}, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "search_more", Description: "Read one bounded content search page. Pass the returned cursor to continue; replaying a cursor rereads that page instead of skipping results. Rechecks fs_find path policy on every call.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in SearchMoreInput) (*mcp.CallToolResult, filesystem.FindResult, error) {
		q, err := sessions.get(in.ID, searchOwner(ctx), false)
		if err != nil {
			return nil, filesystem.FindResult{}, err
		}
		q.Cursor = in.Cursor
		out, err := d.Files.Search(q)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "list_searches", Description: "List your active content search session IDs. Excludes expired sessions without renewing them or sharing search patterns and paths. Use an ID with search_more or search_stop.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, SearchListResult, error) {
		return nil, sessions.list(searchOwner(ctx)), nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "search_stop", Description: "Discard your content search session immediately; idle sessions also expire after ten minutes. No background process or index remains.", Annotations: &mcp.ToolAnnotations{}}, func(ctx context.Context, req *mcp.CallToolRequest, in SearchMoreInput) (*mcp.CallToolResult, map[string]any, error) {
		_, err := sessions.get(in.ID, searchOwner(ctx), true)
		return nil, map[string]any{"id": in.ID, "stopped": err == nil}, err
	}))
}
