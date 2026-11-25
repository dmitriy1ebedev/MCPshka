package toolforge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

const maxReportList = 40

// Report is the tool audit in numbers (ROADMAP P24): what is registered, what
// a client sees, what is deprecated or unused, how ready the backends are and
// which capabilities agents asked for and MCPshka lacks. Counts are exact;
// usage comes from the audit log and is unknown for reads.
type Report struct {
	Registered           int               `json:"registered"`
	Listed               int               `json:"listed"` // in tools/list
	Hidden               int               `json:"hidden"` // callable by name only
	ReadOnly             int               `json:"read_only"`
	Mutating             int               `json:"mutating"`
	MutatingWithContract int               `json:"mutating_with_contract"`
	WithReadiness        int               `json:"with_readiness"`
	Readiness            map[string]int    `json:"readiness"`                      // last observed state → tools
	Deprecated           map[string]string `json:"deprecated,omitempty"`           // duplicate → its replacement
	UnusedMutations      []string          `json:"unused_mutations,omitempty"`     // no audited call in the log window
	MissingCapabilities  []Request         `json:"missing_capabilities,omitempty"` // open requests, newest first
	Truncated            bool              `json:"truncated,omitempty"`
}

// Summarize counts the catalog. hidden and state describe one tool by name;
// deprecated maps a duplicate to its replacement.
func Summarize(tools []Tool, hidden func(string) bool, state func(string) string, deprecated map[string]string, requests []Request) Report {
	r := Report{Registered: len(tools), Readiness: map[string]int{}, Deprecated: deprecated}
	for _, t := range tools {
		if hidden(t.Name) {
			r.Hidden++
		}
		if t.ReadOnly {
			r.ReadOnly++
		} else if r.Mutating++; t.HasContract {
			r.MutatingWithContract++
		}
		if t.HasReady {
			r.WithReadiness++
		}
		r.Readiness[state(t.Name)]++
		if !t.ReadOnly && t.Calls == 0 {
			r.UnusedMutations = append(r.UnusedMutations, t.Name)
		}
	}
	r.Listed = r.Registered - r.Hidden
	sort.Strings(r.UnusedMutations)
	if len(r.UnusedMutations) > maxReportList {
		r.UnusedMutations, r.Truncated = r.UnusedMutations[:maxReportList], true
	}
	for _, q := range requests {
		if q.State == "open" || q.State == "" {
			r.MissingCapabilities = append(r.MissingCapabilities, q)
		}
	}
	sort.Slice(r.MissingCapabilities, func(i, j int) bool { return r.MissingCapabilities[i].At.After(r.MissingCapabilities[j].At) })
	if len(r.MissingCapabilities) > maxReportList {
		r.MissingCapabilities, r.Truncated = r.MissingCapabilities[:maxReportList], true
	}
	return r
}

// Requests loads the recorded capability requests; unreadable files are skipped.
func Requests() []Request {
	files, _ := filepath.Glob(filepath.Join(Dir, "requests", "*.json"))
	var out []Request
	for _, f := range files {
		var q Request
		if raw, err := os.ReadFile(f); err == nil && json.Unmarshal(raw, &q) == nil && q.ID != "" {
			out = append(out, q)
		}
	}
	return out
}
