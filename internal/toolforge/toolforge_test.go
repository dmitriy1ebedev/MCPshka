package toolforge

import (
	"testing"
)

func catalog() map[string]Tool {
	return map[string]Tool{
		"ha_states":        {Name: "ha_states", Description: "List Home Assistant entity states with domain filter", ReadOnly: true, HasReady: true, Calls: 5},
		"ha_state":         {Name: "ha_state", Description: "List Home Assistant entity states with domain filter for one", ReadOnly: true, HasReady: true, Calls: 0},
		"ha_service_call":  {Name: "ha_service_call", Description: "Call a Home Assistant service", HasContract: true, Calls: 3},
		"docker_restart":   {Name: "docker_restart", Description: "Restart a container", Calls: 0},
		"tool_invoke":      {Name: "tool_invoke", ReadOnly: true},
		"x_existing_combo": {Name: "x_existing_combo", ReadOnly: true},
	}
}

func TestAuditFindings(t *testing.T) {
	var tools []Tool
	for _, v := range catalog() {
		tools = append(tools, v)
	}
	kinds := map[string]int{}
	for _, f := range Audit(tools) {
		kinds[f.Kind]++
	}
	if kinds["overlap"] != 1 || kinds["no_contract"] != 1 || kinds["unused"] == 0 || kinds["no_readiness"] == 0 {
		t.Fatalf("%v", kinds)
	}
}

func TestValidateComposite(t *testing.T) {
	c := Composite{Name: "x_light_report", Description: "Read a light state then call a service on it", Inputs: []string{"entity"},
		Steps: []Step{{Tool: "ha_state", Args: map[string]any{"entity_id": "{{input.entity}}"}}, {Tool: "ha_service_call", Args: map[string]any{"data": map[string]any{"entity_id": "{{steps.0.entity_id}}"}}}}}
	v, err := Validate(c, catalog())
	if err != nil || v.ReadOnly {
		t.Fatalf("%v %+v", err, v)
	}
	bad := []Composite{
		{Name: "light", Description: c.Description, Steps: c.Steps},
		{Name: "x_bad_ref", Description: c.Description, Steps: []Step{{Tool: "ha_state", Args: map[string]any{"e": "{{input.nope}}"}}}},
		{Name: "x_forward", Description: c.Description, Inputs: []string{"entity"}, Steps: []Step{{Tool: "ha_state", Args: map[string]any{"e": "{{steps.0.x}}"}}}},
		{Name: "x_recursive", Description: c.Description, Steps: []Step{{Tool: "x_existing_combo"}}},
		{Name: "x_dispatch", Description: c.Description, Steps: []Step{{Tool: "tool_invoke"}}},
		{Name: "x_existing_combo", Description: c.Description, Steps: c.Steps},
	}
	for _, b := range bad {
		if _, err := Validate(b, catalog()); err == nil {
			t.Errorf("%s accepted", b.Name)
		}
	}
}

func TestExpandKeepsTypes(t *testing.T) {
	got := Expand(map[string]any{"n": "{{input.count}}", "s": "id={{steps.0.id}}", "nested": []any{"{{input.count}}"}},
		map[string]any{"count": 3}, []map[string]any{{"id": "abc"}})
	if got["n"] != 3 || got["s"] != "id=abc" || got["nested"].([]any)[0] != 3 {
		t.Fatalf("%v", got)
	}
}

func TestSaveLoadRemove(t *testing.T) {
	Dir = t.TempDir()
	c := Composite{Name: "x_demo_one", Description: "demo composite for the store test", Steps: []Step{{Tool: "ha_states"}}}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(); len(got) != 1 || got[0].Name != c.Name {
		t.Fatalf("%v", got)
	}
	if Remove("ha_states") == nil || Remove(c.Name) != nil {
		t.Fatal("remove rules")
	}
	if SaveRequest(Request{ID: "r1", Goal: "short"}) == nil {
		t.Fatal("thin request accepted")
	}
}

func TestSharedFamilyBoilerplateIsNotOverlap(t *testing.T) {
	boiler := "Owner-authorized bounded storage operation with durable retry key and status in the MCPshka namespace. "
	var tools []Tool
	for _, d := range []string{"Create folders", "Rename entries keeping their identity", "Upload artifacts from staging", "Move between folders", "Trash into recoverable bin"} {
		name := "storage_" + d[:4]
		tools = append(tools, Tool{Name: name, Description: boiler + d, HasContract: true, HasReady: true, Calls: 1})
	}
	for _, f := range Audit(tools) {
		if f.Kind == "overlap" {
			t.Fatalf("shared boilerplate reported as overlap: %v", f.Tools)
		}
	}
}

func TestLoadRequestRoundTrip(t *testing.T) {
	Dir = t.TempDir()
	r := Request{ID: "0123456789abcdef", Goal: "export chosen entities", Missing: "entity filter in ha_exposure_sync", Acceptance: "plan adds exactly the list", State: "open"}
	if err := SaveRequest(r); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRequest(r.ID)
	if err != nil || got.Goal != r.Goal {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := LoadRequest("../etc/passwd"); err == nil {
		t.Fatal("path accepted as id")
	}
}
