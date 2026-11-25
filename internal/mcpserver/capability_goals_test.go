package mcpserver

import (
	"testing"
)

// Goals as the owner and ChatGPT phrase them: the right tool must be among
// tool_resolve's three candidates. Russian inflection and punctuation in
// descriptions used to hide most tools from Russian goals.
func TestRussianGoalsResolveToTheRightTool(t *testing.T) {
	c := catalogClient(t, nil)
	goals := []struct{ goal, want string }{
		{"перезапусти контейнер", "docker_action"},
		{"покажи логи службы", "journal_read"},
		{"разберись что с хостом", "host_triage"},
		{"самолечение контейнеров", "self_heal_status"},
		{"состояние сущности умного дома", "ha_state"},
		{"прочитать файл", "fs_read"},
		{"какие изменения в гите", "git_diff"},
		{"запусти тесты проекта", "project_test"},
		{"дождаться задания", "job_wait"},
		{"отменить задание", "job_cancel"},
		{"влить ветку в main", "integration_ship"},
		{"поручить задачу другому агенту", "agent_task"},
		{"статус задачи агента", "agent_task_status"},
		{"прогресс миссии", "mission_status"},
		{"stalled heartbeat mission", "mission_status"},
		{"нажать кнопку на сайте", "browser_run"},
		{"скриншот страницы", "browser_screenshot"},
		{"следующий пункт роадмапа", "roadmap_next"},
		{"ночной режим", "roadmap_run"},
		{"найти в базе знаний", "knowledge_search"},
		{"запомнить заметку в базе знаний", "knowledge_write"},
		{"справка правила", "mcpshka_guide"},
		{"вызвать инструмент по имени", "tool_invoke"},
		{"перенести проект в repos", "project_repo_preview"},
		{"прогнать тесты на гитверсе", "forge_ci"},
		{"подними MCPshka до 0.8.1 на приставке с CI и откатом", "release_prepare"},
		{"выпустить новую версию mcpshka через подписанный CI artifact", "release_prepare"},
		{"release MCPshka verified GitHub ARM64 install production", "release_prepare"},
		{"при занятом Git lease найти штатный путь его восстановления", "project_lease_admin_stale_preview"},
		{"освободить место на диске", "host_housekeeping_plan"},
		{"проверить конфигурацию home assistant", "ha_check_config"},
		{"статус интеграции", "integration_status"},
		{"резервная копия состояния", "state_backup"},
		{"проверить что прод работает", "project_verify"},
		{"вердикт фазы", "phase_gate"},
		// Phrasings that are not copied from any alias.
		{"рестартани докер контейнер zigbee", "docker_action"},
		{"что в журнале сервиса mcpshka", "journal_read"},
		{"выключи свет в комнате", "ha_service_call"},
		{"сохрани файл с текстом", "fs_write"},
		{"жду пока завершится задание", "job_wait"},
		{"мало места на диске что удалить", "host_housekeeping_plan"},
		{"передай задачу кодексу", "agent_task"},
		{"сделай бэкап", "state_backup"},
		{"какие лимиты у площадок", "providers_status"},
		{"открой сайт гитверса и нажми кнопку", "browser_run"},
	}
	miss := 0
	for _, g := range goals {
		r := catalogCall(t, c, "tool_resolve", map[string]any{"goal": g.goal, "compact": true})
		found := false
		var got []string
		for _, cand := range r["candidates"].([]any) {
			name := cand.(map[string]any)["name"].(string)
			got = append(got, name)
			found = found || name == g.want
		}
		if !found {
			miss++
			t.Errorf("%q: want %s, got %v", g.goal, g.want, got)
		}
	}
	t.Logf("%d/%d Russian goals resolved", len(goals)-miss, len(goals))
}
