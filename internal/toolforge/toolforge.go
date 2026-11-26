// Package toolforge keeps MCPshka's tool catalog healthy: it audits the
// catalog (overlaps, unused tools, mutations without contracts, tools
// without readiness), stores composite tools — chains of existing tools the
// owner approved with Face ID, usable without a release — and records
// requests for capabilities that need new code.
package toolforge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Tool is what the audit needs to know about one registered tool.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	HasContract bool   `json:"has_contract"`
	HasReady    bool   `json:"has_readiness"`
	Calls       int    `json:"calls"` // from the audit log; -1 unknown (reads are not audited)
}

type Finding struct {
	Kind   string   `json:"kind"` // overlap, unused, no_contract, no_readiness
	Tools  []string `json:"tools"`
	Advice string   `json:"advice"`
	// Machine-readable trust fields (MCP-086): a heuristic finding is never
	// by itself a reason to delete, merge or call a tool broken.
	RuleID         string `json:"rule_id"`
	Severity       string `json:"severity"`   // info, warning
	Confidence     string `json:"confidence"` // likelihood the finding is real: LOW, MEDIUM, HIGH
	Evidence       string `json:"evidence"`
	SourceWindow   string `json:"source_window"`
	KnownException string `json:"known_exception,omitempty"`
	SafeMigration  string `json:"safe_migration"`
}

// emergencyTool names rare break-glass/recovery tools whose zero calls mean
// "not needed lately", never "unused and removable".
var emergencyTool = regexp.MustCompile(`(^|_)(rescue|recover|recovery|rollback|restore|fence|abort|break|oob|reconcile|retire|unlock|cancel)(_|$)`)

func finding(kind string, tools []Tool, advice string) Finding {
	f := Finding{Kind: kind, Advice: advice, SourceWindow: "registered catalog + audit.jsonl mutation calls (reads are not audited)"}
	for _, t := range tools {
		f.Tools = append(f.Tools, t.Name)
	}
	switch kind {
	case "no_contract":
		f.RuleID, f.Severity, f.Confidence = "TF001", "warning", "HIGH"
		f.Evidence = "read_only=false and no mutation contract registered"
		f.SafeMigration = "add probe/recovery contract; no behaviour change"
	case "no_readiness":
		f.RuleID, f.Severity, f.Confidence = "TF002", "info", "MEDIUM"
		f.Evidence = "no readiness contract: state is UNPROBED, not broken"
		f.SafeMigration = "add a read-only selftest probe; do not disable the tool"
	case "unused":
		f.RuleID, f.Severity, f.Confidence = "TF003", "info", "LOW"
		f.Evidence = "0 audited calls in the window"
		f.SafeMigration = "observe a longer window and confirm with the owner before deprecation; keep an alias"
		if emergencyTool.MatchString(tools[0].Name) {
			f.KnownException = "rare emergency/recovery tool: zero calls is expected"
			f.Advice = "rare emergency tool: keep it; verify it with a dry-run probe instead"
		}
	case "overlap":
		f.RuleID, f.Severity, f.Confidence = "TF004", "info", "MEDIUM"
		f.Evidence = "description vocabulary Jaccard >= 0.55 within one family and read/write group"
		f.SafeMigration = "sharpen descriptions first; merge only with identical approval/fencing semantics, behind an alias"
		if len(tools) == 2 && tools[0].HasContract != tools[1].HasContract {
			f.Confidence = "LOW"
			f.KnownException = "different mutation-contract class: text overlap does not permit merging"
			f.Advice = "descriptions overlap but the contracts differ: sharpen descriptions, do not merge"
		}
	}
	return f
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'а' && r <= 'я' || r >= '0' && r <= '9')
	}) {
		if len(w) > 3 {
			out[w] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// Audit reports catalog problems; overlap compares description vocabularies
// of tools in the same family (prefix before the first underscore).
func Audit(tools []Tool) []Finding {
	var out []Finding
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	common := familyVocabulary(tools)
	for i := range tools {
		t := tools[i]
		if !t.ReadOnly && !t.HasContract {
			out = append(out, finding("no_contract", []Tool{t}, "mutating tool without a mutation contract: add probe/recovery before agents rely on it"))
		}
		if !t.HasReady {
			out = append(out, finding("no_readiness", []Tool{t}, "no readiness contract: capability_selftest cannot say whether it works"))
		}
		if t.Calls == 0 && !t.ReadOnly {
			out = append(out, finding("unused", []Tool{t}, "no audited calls in the window: candidate for review only, not for removal"))
		}
		fam, _, _ := strings.Cut(t.Name, "_")
		for j := i + 1; j < len(tools); j++ {
			u := tools[j]
			if f2, _, _ := strings.Cut(u.Name, "_"); f2 != fam || t.ReadOnly != u.ReadOnly {
				continue
			}
			if jaccard(distinct(t, common[vocabularyGroup(t)]), distinct(u, common[vocabularyGroup(u)])) >= 0.55 {
				out = append(out, finding("overlap", []Tool{t, u}, "descriptions largely overlap: merge into one tool with a mode, or sharpen both descriptions"))
			}
		}
	}
	return out
}

// Compare vocabulary within the same family and read/mutation group as Audit.
// Storage reads and mutations have separate templates; mixing them dilutes both.
func vocabularyGroup(t Tool) string {
	family, _, _ := strings.Cut(t.Name, "_")
	return fmt.Sprintf("%s:%t", family, t.ReadOnly)
}

// familyVocabulary removes boilerplate shared by most of a comparison group.
func familyVocabulary(tools []Tool) map[string]map[string]bool {
	count := map[string]map[string]int{}
	size := map[string]int{}
	for _, t := range tools {
		fam := vocabularyGroup(t)
		size[fam]++
		if count[fam] == nil {
			count[fam] = map[string]int{}
		}
		for w := range words(t.Description) {
			count[fam][w]++
		}
	}
	out := map[string]map[string]bool{}
	for fam, ws := range count {
		out[fam] = map[string]bool{}
		for w, n := range ws {
			if size[fam] >= 4 && n*3 > size[fam]*2 {
				out[fam][w] = true
			}
		}
	}
	return out
}

func distinct(t Tool, common map[string]bool) map[string]bool {
	out := words(t.Description)
	for w := range common {
		delete(out, w)
	}
	return out
}

// Step calls one existing tool; string args may reference the composite's
// input as {{input.key}} and earlier outputs as {{steps.N.key}}.
type Step struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

type Composite struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Inputs      []string  `json:"inputs"`
	Steps       []Step    `json:"steps"`
	ReadOnly    bool      `json:"read_only"` // all steps read-only
	ApprovedAt  time.Time `json:"approved_at,omitzero"`
	Session     string    `json:"admin_session_id,omitempty"`
}

var (
	compositeName = regexp.MustCompile(`^x_[a-z0-9_]{3,40}$`)
	ref           = regexp.MustCompile(`\{\{(input|steps\.\d+)\.([a-z0-9_]+)\}\}`)
)

// Validate checks a composite against the live catalog: a reserved x_ name,
// existing steps, no recursion through other composites or dispatchers, and
// references only to declared inputs and earlier steps.
func Validate(c Composite, catalog map[string]Tool) (Composite, error) {
	if !compositeName.MatchString(c.Name) {
		return c, errors.New("composite names are x_<3..40 [a-z0-9_]> so they never shadow native tools")
	}
	if _, taken := catalog[c.Name]; taken {
		return c, errors.New(c.Name + " already exists")
	}
	if len(c.Steps) == 0 || len(c.Steps) > 8 || len(c.Description) < 20 || len(c.Description) > 600 {
		return c, errors.New("1..8 steps and a 20..600 byte description")
	}
	c.ReadOnly = true
	for i, s := range c.Steps {
		t, ok := catalog[s.Tool]
		if !ok || strings.HasPrefix(s.Tool, "x_") || s.Tool == "tool_invoke" || s.Tool == "tool_forge" {
			return c, fmt.Errorf("step %d: %q is not a native tool", i, s.Tool)
		}
		c.ReadOnly = c.ReadOnly && t.ReadOnly
		raw, _ := json.Marshal(s.Args)
		for _, m := range ref.FindAllStringSubmatch(string(raw), -1) {
			if m[1] == "input" && !slices.Contains(c.Inputs, m[2]) {
				return c, fmt.Errorf("step %d uses undeclared input %q", i, m[2])
			}
			if strings.HasPrefix(m[1], "steps.") {
				var n int
				fmt.Sscanf(m[1], "steps.%d", &n)
				if n >= i {
					return c, fmt.Errorf("step %d refers to a later step %d", i, n)
				}
			}
		}
	}
	return c, nil
}

// Expand substitutes references in one step's args.
func Expand(args map[string]any, input map[string]any, outputs []map[string]any) map[string]any {
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if m := ref.FindStringSubmatch(x); m != nil && m[0] == x {
				return lookup(m, input, outputs) // whole value: keep its type
			}
			return ref.ReplaceAllStringFunc(x, func(s string) string {
				return fmt.Sprint(lookup(ref.FindStringSubmatch(s), input, outputs))
			})
		case map[string]any:
			out := map[string]any{}
			for k, e := range x {
				out[k] = walk(e)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = walk(e)
			}
			return out
		}
		return v
	}
	return walk(args).(map[string]any)
}

func lookup(m []string, input map[string]any, outputs []map[string]any) any {
	if m[1] == "input" {
		return input[m[2]]
	}
	var n int
	fmt.Sscanf(m[1], "steps.%d", &n)
	if n < len(outputs) {
		return outputs[n][m[2]]
	}
	return nil
}

// Dir holds approved composites and capability requests.
var Dir = "/var/lib/mcpshka/toolforge"

func Save(c Composite) error {
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", " ")
	return os.WriteFile(filepath.Join(Dir, c.Name+".json"), raw, 0o600)
}

func Load() ([]Composite, error) {
	files, _ := filepath.Glob(filepath.Join(Dir, "x_*.json"))
	var out []Composite
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return out, err
		}
		var c Composite
		if json.Unmarshal(raw, &c) == nil && compositeName.MatchString(c.Name) {
			out = append(out, c)
		}
	}
	return out, nil
}

func Remove(name string) error {
	if !compositeName.MatchString(name) {
		return errors.New("only x_ composites can be removed")
	}
	return os.Remove(filepath.Join(Dir, name+".json"))
}

// Request is a capability that needs new code: the agent states the gap and
// the acceptance; implementing it goes through an agent and a release.
type Request struct {
	ID         string    `json:"id"`
	Goal       string    `json:"goal"`
	Missing    string    `json:"missing"`
	Acceptance string    `json:"acceptance"`
	At         time.Time `json:"at"`
	State      string    `json:"state"` // open, assigned, released
}

func SaveRequest(r Request) error {
	if len(r.Goal) < 10 || len(r.Missing) < 10 || len(r.Acceptance) < 10 {
		return errors.New("goal, missing and acceptance need at least 10 bytes each")
	}
	if err := os.MkdirAll(filepath.Join(Dir, "requests"), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(r, "", " ")
	return os.WriteFile(filepath.Join(Dir, "requests", r.ID+".json"), raw, 0o600)
}

// LoadRequest reads one recorded capability request.
func LoadRequest(id string) (Request, error) {
	var r Request
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		return r, errors.New("request id is 16 hex characters")
	}
	raw, err := os.ReadFile(filepath.Join(Dir, "requests", id+".json"))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}

// ForgeWorktree is where a chat builds a requested capability itself.
var ForgeWorktree = "/root/repos/mcpshka-forge"
