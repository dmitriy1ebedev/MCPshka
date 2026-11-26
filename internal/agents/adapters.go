package agents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MCP-098: AI-agnostic agent adapters. Codex and Claude are built in; any
// other provider (DeepSeek, GigaChat, a local model) is an owner-installed
// adapter file AdapterDir/<name>.json naming a CLI and its argument template.
// Tasks, leases, journal and lifecycle stay MCPshka's own, so a task can be
// handed from one provider to another without changing its record.
var AdapterDir = "/etc/mcpshka/agents"

type Adapter struct {
	Name    string   `json:"name"`
	Binary  string   `json:"binary"`
	Args    []string `json:"args"` // {prompt}, {dir}, {report} are substituted
	BuiltIn bool     `json:"built_in,omitempty"`
	Ready   string   `json:"ready"` // READY, BLOCKED
	Reason  string   `json:"reason,omitempty"`
}

var adapterName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

func loadAdapter(name string) (Adapter, error) {
	if !adapterName.MatchString(name) || name == Codex || name == Claude {
		return Adapter{}, errors.New("unknown agent " + name)
	}
	raw, err := os.ReadFile(filepath.Join(AdapterDir, name+".json"))
	if err != nil {
		return Adapter{}, errors.New("unknown agent " + name + ": no adapter " + filepath.Join(AdapterDir, name+".json"))
	}
	var a Adapter
	if err := json.Unmarshal(raw, &a); err != nil || !filepath.IsAbs(a.Binary) || len(a.Args) == 0 {
		return Adapter{}, errors.New("adapter " + name + " invalid: needs absolute binary and args")
	}
	hasPrompt := false
	for _, arg := range a.Args {
		hasPrompt = hasPrompt || strings.Contains(arg, "{prompt}")
	}
	if !hasPrompt {
		return Adapter{}, errors.New("adapter " + name + " never passes {prompt}")
	}
	a.Name = name
	return a, nil
}

func (a Adapter) command(ctx context.Context, dir, prompt, report string) *exec.Cmd {
	args := make([]string, len(a.Args))
	r := strings.NewReplacer("{prompt}", prompt, "{dir}", dir, "{report}", report)
	for i, arg := range a.Args {
		args[i] = r.Replace(arg)
	}
	cmd := exec.CommandContext(ctx, a.Binary, args...)
	cmd.Dir = dir
	return cmd
}

// Adapters lists every provider with observed readiness (binary present).
func Adapters() []Adapter {
	out := []Adapter{{Name: Codex, Binary: "codex", BuiltIn: true}, {Name: Claude, Binary: "claude", BuiltIn: true}}
	entries, _ := os.ReadDir(AdapterDir)
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		a, err := loadAdapter(name)
		if err != nil {
			out = append(out, Adapter{Name: name, Ready: "BLOCKED", Reason: err.Error()})
			continue
		}
		out = append(out, a)
	}
	for i := range out {
		if out[i].Ready != "" {
			continue
		}
		if _, err := exec.LookPath(out[i].Binary); err != nil {
			out[i].Ready, out[i].Reason = "BLOCKED", "binary not found: "+out[i].Binary
		} else {
			out[i].Ready = "READY"
		}
	}
	sort.SliceStable(out[2:], func(i, j int) bool { return out[2+i].Name < out[2+j].Name })
	return out
}
