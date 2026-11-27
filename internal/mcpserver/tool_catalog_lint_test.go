package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mcpshka/internal/toolcontract"
)

// maxCatalogTools is the catalog budget. A new tool fails this lint until the
// budget is raised in the same change, with the reason in the commit: first
// look for an existing tool to extend (owner, 08.11: no useless or dead-end
// tools). Lowering it after removing a duplicate is always welcome.
const maxCatalogTools = 40 // 0.16.0: busiest tools from the lifetime ledger plus discovery.

// dispatchers carry the contracts of the tools they call.
var dispatchers = map[string]bool{"integration_ship": true, "tool_invoke": true}

// TestToolCatalogLint is the standing tool audit: every "next step" MCPshka
// hands an AI (failure next_safe_action, host_triage actions, JSON hints in
// messages, mutation probes/recoveries) must name a registered tool with
// fields its schema has; mutations need a contract; duplicates point to a
// live replacement and are never routed to.
func TestToolCatalogLint(t *testing.T) {
	c := fullCatalogClient(t)
	tools := catalogTools(t, c)
	props := map[string]map[string]any{}
	for _, tool := range tools {
		if len(strings.TrimSpace(tool.Description)) < 20 {
			t.Errorf("%s: description too short to choose it", tool.Name)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]any `json:"properties"`
		}
		_ = json.Unmarshal(raw, &s)
		props[tool.Name] = s.Properties
	}
	if n := len(catalogTools(t, catalogClient(t, nil))); n > maxCatalogTools {
		t.Errorf("tools/list has %d tools, budget %d: extend an existing tool, hide a resolve-only family, or raise maxCatalogTools with a reason", n, maxCatalogTools)
	}
	known := func(name string) bool { _, ok := props[name]; return ok }
	for name := range listedTools {
		if !known(name) {
			t.Errorf("listedTools names unregistered %s", name)
		}
	}

	for old, to := range supersededTools {
		if !known(to) {
			t.Errorf("superseded %s points to unregistered %s", old, to)
		}
	}
	for name, ct := range toolcontract.Contracts {
		if !known(name) {
			t.Errorf("contract for unregistered tool %s", name)
		}
		for _, ref := range []string{ct.Probe, ct.Recovery} {
			if ref != "bounded_expiry" && ref != "APPROVAL_REQUIRED" && !known(ref) {
				t.Errorf("contract %s: probe/recovery %s is not a tool", name, ref)
			}
		}
	}
	for _, tool := range tools {
		if tool.Annotations != nil && !tool.Annotations.ReadOnlyHint && !dispatchers[tool.Name] {
			if _, ok := toolcontract.Contracts[tool.Name]; !ok {
				t.Errorf("mutating %s has no probe/recovery contract", tool.Name)
			}
		}
	}

	action := regexp.MustCompile(`\bTool:\s*"([a-z]+_[a-z_]+)"`)
	argMap := regexp.MustCompile(`^[^}]*?(?:Args|Arguments):\s*map\[string\]any\{([^}]*)\}`)
	argKey := regexp.MustCompile(`"([a-z_]+)":`)
	jsonHint := regexp.MustCompile(`"tool\\?"\s*:\s*\\?"([a-z]+_[a-z_]+)`)
	checked := 0
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		at := func(i int) string { return path + ":" + itoa(strings.Count(src[:i], "\n")+1) }
		for _, m := range action.FindAllStringSubmatchIndex(src, -1) {
			name := src[m[2]:m[3]]
			if name == "owner" {
				continue
			}
			checked++
			if !known(name) {
				t.Errorf("%s: next step names unregistered tool %s", at(m[0]), name)
				continue
			}
			if _, old := supersededTools[name]; old {
				t.Errorf("%s: next step routes to superseded %s, use %s", at(m[0]), name, supersededTools[name])
			}
			// The literal's own Args follow within a few lines; a greedy tail in
			// the regex would swallow the next Tool: field, so cut it here.
			if a := argMap.FindStringSubmatch(src[m[3]:min(len(src), m[3]+600)]); a != nil {
				for _, k := range argKey.FindAllStringSubmatch(a[1], -1) {
					if _, ok := props[name][k[1]]; !ok {
						t.Errorf("%s: %s has no field %q", at(m[0]), name, k[1])
					}
				}
			}
		}
		for _, m := range jsonHint.FindAllStringSubmatchIndex(src, -1) {
			checked++
			if name := src[m[2]:m[3]]; !known(name) {
				t.Errorf("%s: JSON hint names unregistered tool %s", at(m[0]), name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 33 Tool: fields and 2 JSON hints on 08.11; far fewer means the
	// scan stopped matching, not that the code got cleaner.
	if checked < 30 {
		t.Fatalf("lint saw only %d next-step links; the source scan is broken", checked)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
