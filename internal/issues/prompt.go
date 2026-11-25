package issues

import (
	"fmt"
	"strings"
)

// Prompt is the agent task text for a card (issue_dispatch).
func Prompt(i Issue, related []Summary, experience []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Задача: исправить проблему MCPshka %s (%s) — %s.\n\n", i.ID, i.Priority, i.Title)
	b.WriteString("Порядок: mcpshka_guide topic=rules; knowledge_search по теме; воспроизвести; исправить с регрессионным тестом; project_test; project_commit. Затем issue_update id=" + i.ID + " status=fixed commit=<sha> (release — если выпущено). Карточку не закрывай: закрытие — только приёмкой (issue_accept) с доказательством.\n\n")
	b.WriteString("## Карточка\n" + i.Body + "\n")
	if len(related) > 0 {
		b.WriteString("\n## Похожие карточки (проверь, не та же ли причина)\n")
		for _, r := range related {
			fmt.Fprintf(&b, "- %s [%s] %s\n", r.ID, r.Status, r.Title)
		}
	}
	if len(experience) > 0 {
		b.WriteString("\n## Опыт из базы знаний\n")
		for _, e := range experience {
			b.WriteString("- " + e + "\n")
		}
	}
	out := b.String()
	if len(out) > 60<<10 {
		out = out[:60<<10]
	}
	return out
}

// Related lists up to 5 other cards similar to i.
func (s *Store) Related(i Issue) []Summary {
	similar, _ := s.Similar(i.Title, 6)
	var out []Summary
	for _, c := range similar {
		if c.ID != i.ID && len(out) < 5 {
			out = append(out, c)
		}
	}
	return out
}
