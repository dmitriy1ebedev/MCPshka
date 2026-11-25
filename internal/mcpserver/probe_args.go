package mcpserver

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/toolcontract"
)

// A mutation's before/after probe is an ordinary read-only tool. Hand-written
// cases in toolcontract.ProbeArguments map inputs only for some probes; the
// rest were called with no arguments, so every new contract whose probe takes
// an id or path was held by «before probe failed» (MCP-130 issue_get,
// MCP-132 phase_gate). The probe's own schema now names what it takes: each
// of its fields still missing is copied from the mutation input of the same
// name.

type toolFields struct {
	props    map[string]bool
	required []string
}

func schemaFields(t *mcp.Tool) toolFields {
	f := toolFields{props: map[string]bool{}}
	if t == nil || t.InputSchema == nil {
		return f
	}
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return f
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return f
	}
	for k := range s.Properties {
		f.props[k] = true
	}
	f.required = s.Required
	return f
}

// fillProbeArgs adds the probe's schema fields that the mutation input
// carries under the same name. Explicit mappings win; the retry key is the
// mutation's own and never becomes the probe's, and so is its action: a
// fence call's action="fence" made its status probe refuse (MCP-137).
func (r *toolRegistry) fillProbeArgs(probe string, args, in map[string]any) map[string]any {
	f := schemaFields(r.lookupTool(probe).tool)
	if len(f.props) == 0 {
		return args
	}
	if args == nil {
		args = map[string]any{}
	}
	for k := range f.props {
		if k == "idempotency_key" || k == "action" {
			continue
		}
		if _, set := args[k]; set {
			continue
		}
		if v, ok := in[k]; ok && v != nil {
			args[k] = v
		}
	}
	return args
}

// allProbeGaps checks every registered mutation's before-probe the way the
// handler calls it: explicit mapping, then schema fill from a full input.
func (r *toolRegistry) allProbeGaps() []string {
	var out []string
	view := r.registryView()
	names := make([]string, 0, len(view.tools))
	for name, readOnly := range view.tools {
		if !readOnly {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		c, ok := view.contracts[name]
		if !ok || view.catalog[name] == nil {
			continue
		}
		in := map[string]any{}
		for k := range schemaFields(view.catalog[name]).props {
			in[k] = "x"
		}
		probe, args, err := toolcontract.ProbeArguments(name, c.Probe, in, map[string]any{}, true)
		if err != nil {
			continue
		}
		args = r.fillProbeArgs(probe, args, in)
		if gaps := r.probeGaps(name, probe, args); len(gaps) > 0 {
			out = append(out, name+"→"+probe+" lacks "+strings.Join(gaps, ","))
		}
	}
	return out
}

// probeGaps lists required probe fields a mutation can never supply: not in
// the explicit mapping and not a field of its own input. Checked over the
// whole catalog by a test, so a new contract cannot ship a probe it cannot
// call.
func (r *toolRegistry) probeGaps(name, probe string, args map[string]any) []string {
	own := schemaFields(r.lookupTool(name).tool)
	var gaps []string
	for _, k := range schemaFields(r.lookupTool(probe).tool).required {
		if _, set := args[k]; set {
			continue
		}
		if own.props[k] {
			continue
		}
		gaps = append(gaps, k)
	}
	return gaps
}
