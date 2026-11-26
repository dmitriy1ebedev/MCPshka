// Package catalogdigest hashes the public semantics of a tool catalog.
package catalogdigest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sort"
)

func Hash(catalog map[string]*mcp.Tool) string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	type entry struct {
		Name        string               `json:"name"`
		Input       any                  `json:"input_schema"`
		Output      any                  `json:"output_schema"`
		Description string               `json:"description"`
		Annotations *mcp.ToolAnnotations `json:"annotations"`
	}
	entries := make([]entry, 0, len(names))
	for _, name := range names {
		t := catalog[name]
		entries = append(entries, entry{t.Name, t.InputSchema, t.OutputSchema, t.Description, t.Annotations})
	}
	// Canonical form: a JSON round trip sorts every object key, so the server's
	// typed schemas and a client's decoded tools/list hash the same.
	raw, err := json.Marshal(entries)
	var canon any
	if err == nil {
		err = json.Unmarshal(raw, &canon)
	}
	if err == nil {
		raw, err = json.Marshal(canon)
	}
	if err != nil {
		panic("invalid registered tool metadata")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
