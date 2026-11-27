package codemode

import (
	"encoding/json"
	"sort"
	"strings"
)

// Declaration renders a tool's input JSON Schema as one TypeScript call
// signature: the same contract in fewer tokens (S5.CM, as in
// Cloudflare Code Mode). Unknown schema shapes degrade to any, never fail.
func Declaration(name string, schema any) string {
	var s map[string]any
	raw, _ := json.Marshal(schema)
	_ = json.Unmarshal(raw, &s)
	return "declare function " + name + "(args: " + tsType(s, "") + "): Promise<any>;"
}

func tsType(s map[string]any, indent string) string {
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		parts := make([]string, len(enum))
		for i, v := range enum {
			b, _ := json.Marshal(v)
			parts[i] = string(b)
		}
		return strings.Join(parts, " | ")
	}
	var kinds []string
	switch t := s["type"].(type) {
	case string:
		kinds = []string{t}
	case []any:
		for _, k := range t {
			if k, ok := k.(string); ok {
				kinds = append(kinds, k)
			}
		}
	}
	var parts []string
	for _, k := range kinds {
		switch k {
		case "string", "boolean", "null":
			parts = append(parts, k)
		case "integer", "number":
			parts = append(parts, "number")
		case "array":
			item := "any"
			if items, ok := s["items"].(map[string]any); ok {
				item = tsType(items, indent)
			}
			if strings.Contains(item, " | ") {
				item = "(" + item + ")"
			}
			parts = append(parts, item+"[]")
		case "object":
			parts = append(parts, tsObject(s, indent))
		}
	}
	if len(parts) == 0 {
		return "any"
	}
	return strings.Join(parts, " | ")
}

func tsObject(s map[string]any, indent string) string {
	props, _ := s["properties"].(map[string]any)
	if len(props) == 0 {
		if extra, ok := s["additionalProperties"].(map[string]any); ok {
			return "Record<string, " + tsType(extra, indent) + ">"
		}
		return "Record<string, any>"
	}
	required := map[string]bool{}
	if list, ok := s["required"].([]any); ok {
		for _, n := range list {
			if n, ok := n.(string); ok {
				required[n] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	inner := indent + "  "
	var b strings.Builder
	b.WriteString("{\n")
	for _, n := range names {
		p, _ := props[n].(map[string]any)
		if d, _ := p["description"].(string); d != "" {
			b.WriteString(inner + "/** " + strings.ReplaceAll(strings.Join(strings.Fields(d), " "), "*/", "* /") + " */\n")
		}
		opt := "?"
		if required[n] {
			opt = ""
		}
		b.WriteString(inner + n + opt + ": " + tsType(p, inner) + ";\n")
	}
	b.WriteString(indent + "}")
	return b.String()
}
