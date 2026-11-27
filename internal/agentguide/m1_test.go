package agentguide

import (
	"strings"
	"testing"
)

func TestM1GuideRoutes(t *testing.T) {
	rules, _ := Read("rules")
	if !strings.HasPrefix(rules, DevelopmentInstructions) {
		t.Fatal("missing first instruction")
	}
	for _, topic := range []string{"rules", "agents"} {
		s, _ := Read(topic)
		for _, need := range []string{"roadmap_run", "roadmap_run_status", "ff-merge", "sealed"} {
			if !strings.Contains(s, need) {
				t.Fatalf("%s lacks %s", topic, need)
			}
		}
	}
	agents, _ := Read("agents")
	for _, need := range []string{"reset_at", "resume_of", "resumed_by", "8 ч", "3 продолжений"} {
		if !strings.Contains(agents, need) {
			t.Fatal("quota recovery guide missing", need)
		}
	}
	release, _ := Read("release")
	if !strings.Contains(release, "release_apply") || strings.Contains(release, "пока нет `release_apply`") || strings.Contains(release, "bootstrap-stage.py") {
		t.Fatal("obsolete release route")
	}
}

// MCP-183: release guidance must discover and prefer the owned autorelease
// instead of stopping at a separately blocked, client-side integration call.
func TestReleaseGuideAutoreleasePreferredAndSafeguarded(t *testing.T) {
	release, err := Read("release")
	if err != nil {
		t.Fatal(err)
	}
	auto := strings.Index(release, "## Сначала выбери штатный маршрут")
	manual := strings.Index(release, "## Ручной выпуск")
	if auto < 0 || manual <= auto {
		t.Fatal("autorelease must precede the manual release path")
	}
	for _, want := range []string{
		"release_apply action=queue",
		"release_apply_status.queue",
		"tool_resolve",
		"admin_session_status",
		"store_id",
		"idempotency_key",
		"build_permit",
		"test_permit",
		"git dirty",
		"CI",
		"Face ID",
		"queue.stage=DONE",
		"installed_version=version",
		"project_verify",
		"rollback",
		"Не обходи отказ",
		"не создавай дубликат",
	} {
		if !strings.Contains(release[:manual], want) {
			t.Errorf("autorelease guidance lacks %q", want)
		}
	}
}

// 14.11: ChatGPT spent half an hour on "I will not bypass" around an IPK the
// pipeline had built; the guide must route a dead end, not invite deliberation.
func TestRulesActFirstWithoutBypassTalk(t *testing.T) {
	rules, _ := Read("rules")
	for _, need := range []string{"**Делай сразу.**", "next_safe_action", "allow_downgrade", "webos_deploy", "открыта MCP-NNN"} {
		if !strings.Contains(rules, need) {
			t.Fatal("rules lack", need)
		}
	}
	for _, bad := range []string{"не обходить", "Не обходи", "ради обхода"} {
		if strings.Contains(rules, bad) {
			t.Fatal("prohibition wording invites deliberation:", bad)
		}
	}
	if !strings.Contains(DevelopmentInstructions, "делай сразу") {
		t.Fatal("server instructions lack the act-first line")
	}
}
