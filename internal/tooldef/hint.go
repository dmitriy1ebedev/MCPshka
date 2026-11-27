package tooldef

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// SchemaHint turns a schema refusal into the next correct call: the
// accepted fields, the required ones, a close match for each unknown field
// and a minimal example. A caller (an AI client or the brain) should not
// need tool_resolve to recover from a misspelt parameter.
func SchemaHint(s *jsonschema.Schema, args map[string]any) string {
	if s == nil || len(s.Properties) == 0 {
		return "this tool takes no arguments"
	}
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	var unknown []string
	for k := range args {
		if _, ok := s.Properties[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		if near := closest(k, names); near != "" {
			b.WriteString("«" + k + "» → «" + near + "»; ")
		}
	}
	shown := names
	if len(shown) > 24 {
		shown = shown[:24]
	}
	b.WriteString("accepted: " + strings.Join(shown, ", "))
	if len(names) > len(shown) {
		b.WriteString(", …")
	}
	if len(s.Required) > 0 {
		b.WriteString("; required: " + strings.Join(s.Required, ", "))
	}
	example := map[string]any{}
	for _, r := range s.Required {
		example[r] = sample(s.Properties[r])
	}
	if raw, err := json.Marshal(example); err == nil && len(raw) < 400 {
		b.WriteString("; example: " + string(raw))
	}
	return b.String()
}

func sample(p *jsonschema.Schema) any {
	if p == nil {
		return "…"
	}
	if len(p.Enum) > 0 {
		return p.Enum[0]
	}
	t := p.Type
	if t == "" && len(p.Types) > 0 {
		t = p.Types[0]
	}
	switch t {
	case "integer", "number":
		return 1
	case "boolean":
		return true
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	}
	return "…"
}

// closest is the accepted name a misspelt field most likely meant: same
// word inside it, or within two edits.
func closest(k string, names []string) string {
	best, dist := "", 3
	lk := strings.ToLower(k)
	for _, n := range names {
		ln := strings.ToLower(n)
		if strings.Contains(ln, lk) || strings.Contains(lk, ln) {
			return n
		}
		if d := edits(lk, ln); d < dist {
			best, dist = n, d
		}
	}
	return best
}

func edits(a, b string) int {
	if len(a) > 40 || len(b) > 40 {
		return 99
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
