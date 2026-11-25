package policy

import "testing"

// MCP-034: a refused interpreter names the sandboxed route.
func TestRefusedInterpreterNamesRoute(t *testing.T) {
	p := &Policy{AllowedCommandClasses: map[CommandClass]bool{ClassReadOnly: true, ClassMutating: true}}
	d, err := p.DecideCommand("node", []string{"build.js"})
	if err == nil || d.Allowed || d.Route == "" {
		t.Fatalf("decision %+v err %v", d, err)
	}
	ok, _ := p.DecideCommand("ls", nil)
	if !ok.Allowed || ok.Route != "" {
		t.Fatalf("allowed command got a route: %+v", ok)
	}
}
