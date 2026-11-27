package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// guideFields are result fields and values the guides quote in backticks;
// anything else snake_case there must be a live tool or one of its inputs.
var guideFields = map[string]bool{"also_open": true, "exit_code": true, "host_pressure": true, "last_seen": true,
	"next_action": true, "observed_completed": true, "phase_rules": true, "installed_version": true,
	"roadmap_style": true, "next_safe_action": true, "retry_seconds": true, "rollback_from": true}

var guideToken = regexp.MustCompile("`([a-z][a-z0-9]*_[a-z0-9_]*)[ `]")

// TestGuidesNameLiveTools is catalog completeness for what an AI reads
// first (ROADMAP S5): every tool the MCPshka guides and skills tell it to
// call is registered and not superseded; a renamed or removed tool fails CI
// instead of sending the next AI to a dead end.
func TestGuidesNameLiveTools(t *testing.T) {
	tools := catalogTools(t, fullCatalogClient(t))
	known, inputs := map[string]bool{}, map[string]bool{}
	for _, tool := range tools {
		known[tool.Name] = true
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]any `json:"properties"`
		}
		_ = json.Unmarshal(raw, &s)
		for p := range s.Properties {
			inputs[p] = true
		}
	}
	files, _ := filepath.Glob("../agentguide/guide/*.md")
	skills, _ := filepath.Glob("../../skills/*/SKILL.md")
	files = append(files, skills...)
	if len(files) < 5 {
		t.Fatalf("guides not found: %v", files)
	}
	var bad []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range guideToken.FindAllStringSubmatch(string(raw), -1) {
			name := m[1]
			if to, old := supersededTools[name]; old {
				bad = append(bad, filepath.Base(filepath.Dir(f))+"/"+filepath.Base(f)+": "+name+" is superseded by "+to)
			} else if !known[name] && !inputs[name] && !guideFields[name] {
				bad = append(bad, filepath.Base(f)+": "+name+" is no tool, input or listed field")
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("guides name dead tools (fix the guide, or list a result field in guideFields):\n%s", strings.Join(bad, "\n"))
	}
}
