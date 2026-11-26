package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/agentguide"
)

// The agent guide ships inside the binary (internal/agentguide) so every AI
// client reads the same operating rules from MCPshka itself.

type GuideInput struct {
	Topic string `json:"topic,omitempty" jsonschema:"index (default), rules, roadmap, recovery, tests, host, providers, tools."`
}

type GuideResult struct {
	Topic  string   `json:"topic"`
	Text   string   `json:"text"`
	Topics []string `json:"topics"`
}

// guideCategories groups tools for the generated "tools" index; the first
// matching prefix wins, unmatched tools land in "other".
var guideCategories = []struct{ name, prefixes, words string }{
	{"Проект и roadmap", "project_ phase_gate phase_checkpoint workflow_ roadmap_", "проект репозиторий репо фаза этап план роадмап сборка собрать тест проверка деплой выкатить блокировка аренда лиз lease ночной"},
	{"Файлы и git", "fs_ git_ artifact_", "файл файлы папка каталог прочитать записать правка патч гит коммит дифф изменения загрузить загрузка выгрузить артефакт"},
	{"Задания и тесты", "job_ exec_ runner_", "задание задача команда запустить выполнить вывод результат ожидать отменить тесты прогон раннер облачная ide гигаиде gigaide codespaces ноутбук внешняя машина разгрузить"},
	{"Интеграция в main", "integration_ executor_ forge_ci", "интеграция влить вливание ветка мейн main одобрить конвейер очередь gitverse гитверс moshub мосхаб ci снимок"},
	{"Деплой и релиз", "release_ broker_install state_ protected_core", "релиз выпуск версия деплой выкатить откат состояние бэкап резервная копия восстановить защищённое ядро брокер"},
	{"Хост", "host_ service_ docker_ journal_ health_ network_ device_ control_path traffic_ rescue_ diag_", "хост приставка сервер сервис служба контейнер докер журнал логи перезапуск перезапустить здоровье сеть интернет диск память нагрузка устройство трафик спасение диагностика диаг разберись"},
	{"Умный дом", "ha_", "умный дом home assistant хоум ассистант свет лампа датчик автоматизация сущность выключатель"},
	{"Агенты и облако", "providers_ cloud_ codex_ agent_", "агент кодекс codex claude клод нейросеть поручить делегировать облако квота лимит провайдер сессия"},
	{"Документы и облака", "doc_ storage_ backup_", "документ таблица эксель excel word ворд ods xlsx docx odt csv txt макрос формула формат оформить диаграмма облако диск гугл яндекс дропбокс скачать загрузить бэкап"},
	{"Сайты без API", "browser_", "браузер сайт страница кнопка нажать войти скриншот снимок экрана веб"},
	{"Администрирование (Face ID)", "admin_ root_task_ approval_ mutation_ idempotency_ interaction_recovery", "администрирование админ face фейс подтверждение одобрение разрешение права root рут восстановление повтор потерянный ответ"},
	{"Проблемы", "issue_", "проблема проблемы баг ошибка карточка реестр дефект сбой исправить принять приёмка"},
	{"Знания и поиск", "knowledge_ search_ index_", "знания база заметка запомнить найти поиск индекс"},
	{"Справка", "mcpshka_guide capabilities_ tool_ client_catalog", "справка помощь правила инструмент инструменты каталог найти вызвать"},
}

func (r *toolRegistry) toolIndex() string {
	groups := map[string][]string{}
	view := r.snapshotRegistry()
	for _, name := range view.names() {
		cat := "Прочее"
		for _, c := range guideCategories {
			for _, p := range strings.Fields(c.prefixes) {
				if strings.HasPrefix(name, p) {
					cat = c.name
					break
				}
			}
			if cat != "Прочее" {
				break
			}
		}
		desc := view.catalog[name].Description
		if i := strings.IndexAny(desc, ".:"); i > 0 && i < 140 {
			desc = desc[:i]
		} else if len(desc) > 140 {
			desc = desc[:140] + "…"
		}
		groups[cat] = append(groups[cat], "- `"+name+"` — "+desc)
	}
	var b strings.Builder
	b.WriteString("# Инструменты MCPshka по категориям\n")
	order := append([]string{}, func() []string {
		var o []string
		for _, c := range guideCategories {
			o = append(o, c.name)
		}
		return append(o, "Прочее")
	}()...)
	for _, cat := range order {
		items := groups[cat]
		if len(items) == 0 {
			continue
		}
		sort.Strings(items)
		fmt.Fprintf(&b, "\n## %s\n%s\n", cat, strings.Join(items, "\n"))
	}
	return b.String()
}

func registerGuideTool(r *toolRegistry) {
	closed := false
	addTool(r, &mcp.Tool{
		Name:        "mcpshka_guide",
		Description: "Справка для ИИ-агента: правила работы (не останавливаться ради отчётов, вердикты брать у MCPshka, повтор с тем же idempotency_key), цикл фазы roadmap, восстановление, тесты, хост, площадки, выпуск версии (release), работа агентов (agents), самодостройка инструментов из чата (forge), разгрузка H96 — CI GitVerse и раннеры (runners) и индекс инструментов по категориям. Начни с topic=rules.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in GuideInput) (*mcp.CallToolResult, GuideResult, error) {
		topics := []string{"index", "rules", "roadmap", "recovery", "tests", "host", "providers", "release", "agents", "forge", "runners", "tools"}
		topic := strings.TrimSpace(in.Topic)
		if topic == "" {
			topic = "index"
		}
		out := GuideResult{Topic: topic, Topics: topics}
		if topic == "tools" {
			out.Text = r.toolIndex()
			return nil, out, nil
		}
		text, err := agentguide.Read(topic)
		if err != nil {
			return nil, GuideResult{}, fmt.Errorf("unknown topic %q; topics: %s", topic, strings.Join(topics, ", "))
		}
		out.Text = text
		return nil, out, nil
	})
}
