package catalogdigest

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Capability entries are disposable catalog projections, never file indexes or grants.
type Capability struct {
	ID          string    `json:"id"`
	SchemaHash  string    `json:"schema_hash"`
	ToolHash    string    `json:"tool_hash"`
	Aliases     string    `json:"aliases,omitempty"`
	Description string    `json:"description"`
	Trust       string    `json:"trust"`
	Executable  bool      `json:"executable"`
	ObservedAt  time.Time `json:"observed_at"`
}
type Delta struct {
	Revision string       `json:"revision"`
	Changed  []Capability `json:"changed"`
	Removed  []string     `json:"removed"`
	Cursor   int          `json:"cursor"`
	Partial  bool         `json:"partial"`
}

// Schema hashes use the same canonicalization as the full tools/list digest.
func Schema(t *mcp.Tool) string {
	return Hash(map[string]*mcp.Tool{t.Name: {Name: t.Name, InputSchema: t.InputSchema, OutputSchema: t.OutputSchema}})
}

// CatalogDelta receives only the caller-authorized registry snapshot.
func CatalogDelta(catalog map[string]*mcp.Tool, aliases map[string]string, known map[string]string, cursor, limit int, now time.Time, expected string) (Delta, error) {
	out := Delta{Revision: Hash(catalog), Changed: []Capability{}, Removed: []string{}}
	if len(known) > 1024 || cursor < 0 || limit < 1 || limit > 32 {
		return out, errors.New("invalid catalog delta bounds")
	}
	if (cursor > 0 || expected != "") && expected != out.Revision {
		return out, errors.New("catalog changed: restart delta from cursor zero")
	}
	for name, hash := range known {
		if len(name) > 128 || !digestValid(hash) {
			return out, errors.New("invalid catalog baseline")
		}
	}
	names := []string{}
	for name := range catalog {
		names = append(names, name)
	}
	for name := range known {
		if _, ok := catalog[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if cursor > len(names) {
		return out, errors.New("invalid catalog cursor")
	}
	bytes := 0
	for i := cursor; i < len(names); i++ {
		name := names[i]
		t, exists := catalog[name]
		if exists {
			entry := Capability{ID: name, SchemaHash: Schema(t), ToolHash: Hash(map[string]*mcp.Tool{name: t}), Aliases: aliases[name], Description: t.Description, Trust: "UNTRUSTED", ObservedAt: now.UTC()}
			if known[name] == entry.ToolHash {
				out.Cursor = i + 1
				continue
			}
			raw, err := json.Marshal(entry)
			if err != nil {
				return out, err
			}
			if len(raw) > 24576 {
				return out, errors.New("catalog entry exceeds output bound; resolve its schema lazily")
			}
			if bytes+len(raw) > 24576 || len(out.Changed)+len(out.Removed) >= limit {
				out.Partial = true
				break
			}
			bytes += len(raw)
			out.Changed = append(out.Changed, entry)
		} else {
			raw, _ := json.Marshal(name)
			if bytes+len(raw) > 24576 || len(out.Changed)+len(out.Removed) >= limit {
				out.Partial = true
				break
			}
			bytes += len(raw)
			out.Removed = append(out.Removed, name)
		}
		out.Cursor = i + 1
	}
	return out, nil
}

func digestValid(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
