package mcpserver

import "testing"

// MCP-041/044: one call turns an owner task into an ordered plan with the
// Face ID cost; an impossible step is MISSING, not silently dropped.
func TestTaskPlanBuildsOrderedPlanWithFaceIDCost(t *testing.T) {
	c := catalogClient(t, nil)
	r := catalogCall(t, c, "task_plan", map[string]any{"goal": "покажи логи службы, затем перезапусти контейнер; закажи пиццу на дом"})
	steps, _ := r["steps"].([]any)
	if len(steps) != 3 || r["state"] != "PARTIAL" {
		t.Fatalf("plan: %v", r)
	}
	first := steps[0].(map[string]any)
	if first["tool"] != "journal_read" || first["verify"] == nil {
		t.Fatalf("first step: %v", first)
	}
	if steps[1].(map[string]any)["tool"] != "docker_action" {
		t.Fatalf("second step: %v", steps[1])
	}
	if last := steps[2].(map[string]any); last["missing"] != true || last["fallback"] == nil {
		t.Fatalf("pizza must be missing: %v", last)
	}
	if n, _ := r["face_id_prompts"].(float64); n > 1 {
		t.Fatalf("everyday chain costs %v Face ID prompts", n)
	}
}

func TestTaskPlanSelfReleaseChoosesSignedPairedWorkflow(t *testing.T) {
	c := catalogClient(t, nil)
	r := catalogCall(t, c, "task_plan", map[string]any{"goal": "подними MCPshka до 0.8.1 на приставке с CI и откатом"})
	if r["state"] != "READY" {
		t.Fatalf("self-release recipe unavailable: %v", r)
	}
	steps, ok := r["steps"].([]any)
	want := []string{"project_context", "integration_ship", "integration_approve", "integration_ship", "release_prepare", "release_status", "project_deploy", "project_deploy_status", "project_verify"}
	if !ok || len(steps) != len(want) {
		t.Fatalf("wrong paired release chain: %v", r)
	}
	for i, w := range want {
		step, ok := steps[i].(map[string]any)
		if !ok || step["tool"] != w || step["missing"] == true {
			t.Fatalf("step %d: want %s, got %v", i, w, steps[i])
		}
	}
	if prompts, _ := r["face_id_prompts"].(float64); prompts != 1 {
		t.Fatalf("signed integration needs exactly one owner Face ID session, got %v", prompts)
	}
}

func TestTaskPlanReleaseRespectsMultipleOwnerClauses(t *testing.T) {
	c := catalogClient(t, nil)
	r := catalogCall(t, c, "task_plan", map[string]any{"goal": "подними MCPshka до 0.8.1, затем покажи логи службы"})
	steps, ok := r["steps"].([]any)
	if !ok || len(steps) != 2 {
		t.Fatalf("owner's follow-on clause was lost: %v", r)
	}
	if steps[0].(map[string]any)["tool"] != "release_prepare" || steps[1].(map[string]any)["tool"] != "journal_read" {
		t.Fatalf("follow-on order changed: %v", steps)
	}
}
