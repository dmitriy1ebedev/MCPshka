package mcpserver

import "testing"

// MCP-085: offline intent→tool evaluation with negative cases. Goals with no
// capability must say CAPABILITY_MISSING instead of offering a random tool,
// and recall/top-1 precision of the positive set are measured, not assumed.
func TestIntentEvalPrecisionRecallAndNegatives(t *testing.T) {
	c := catalogClient(t, nil)
	positives := []struct{ goal, want string }{
		{"перезапусти контейнер", "docker_action"},
		{"покажи логи службы", "journal_read"},
		{"запусти тесты проекта", "project_test"},
		{"влить ветку в main", "integration_ship"},
		{"поручить задачу другому агенту", "agent_task"},
		{"список задач агентов", "agent_task_list"},
		{"освободить место на диске", "host_housekeeping_plan"},
		{"резервная копия состояния", "state_backup"},
		{"какие лимиты у площадок", "providers_status"},
		{"прочитать файл", "fs_read"},
		{"restart docker container", "docker_action"},
		{"service logs", "journal_read"},
		{"run project tests", "project_test"},
		{"integration ship to main", "integration_ship"},
		{"cancel agent task", "agent_task_cancel"},
		{"backup state", "state_backup"},
		{"provider quota", "providers_status"},
		{"read file", "fs_read"},
		{"write file", "fs_write"},
		{"job status", "job_status"},
		{"wait job completion", "job_wait"},
		{"VPS provider status", "providers_status"},
	}
	negatives := []string{
		"закажи пиццу на дом",
		"переведи деньги на карту маме",
		"напиши стихотворение про осень",
		"подключить новый VPS SSH Tailscale VPN",
		"добавить VPS",
		"connect a new VPS using SSH Tailscale VPN",
		"enroll a new SSH host",
	}
	top1, recall := 0, 0
	for _, g := range positives {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": g.goal, "compact": true})
		cands, _ := r["candidates"].([]any)
		for i, cand := range cands {
			if cand.(map[string]any)["name"] == g.want {
				recall++
				if i == 0 {
					top1++
				}
				break
			}
		}
	}
	falsePositive := 0
	for _, goal := range negatives {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": goal, "compact": true})
		if r["code"] != "CAPABILITY_MISSING" {
			falsePositive++
			t.Errorf("%q: no tool does this, got code %v", goal, r["code"])
		}
	}
	t.Logf("intent eval: recall %d/%d, top-1 %d/%d, false positives %d/%d", recall, len(positives), top1, len(positives), falsePositive, len(negatives))
	if recall != len(positives) {
		t.Fatalf("known supported intents missing: recall %d/%d", recall, len(positives))
	}
}
