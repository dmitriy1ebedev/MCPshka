package mcpserver

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/agentguide"
)

func TestGuideTopicsAndToolIndex(t *testing.T) {
	for _, topic := range []string{"index", "rules", "roadmap", "recovery", "tests", "host", "providers"} {
		raw, err := agentguide.Read(topic)
		if err != nil || len(raw) < 200 {
			t.Fatal("guide topic missing", topic, err)
		}
	}
	r := &toolRegistry{catalog: map[string]*mcp.Tool{
		"phase_gate": {Name: "phase_gate", Description: "One verdict for a phase. More."},
		"ha_states":  {Name: "ha_states", Description: "Home Assistant entities."},
		"zz_unknown": {Name: "zz_unknown", Description: "x"},
	}}
	idx := r.toolIndex()
	for _, want := range []string{"## Проект и roadmap", "`phase_gate` — One verdict for a phase", "## Умный дом", "## Прочее"} {
		if !strings.Contains(idx, want) {
			t.Fatalf("tool index lacks %q:\n%s", want, idx)
		}
	}
	if !strings.Contains(capabilityInstructions, "mcpshka_guide") {
		t.Fatal("server instructions must point agents at the guide")
	}
}
