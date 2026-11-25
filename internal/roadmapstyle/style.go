// Package roadmapstyle is the shared ROADMAP and wording contract of every
// project MCPshka runs (0.11.0 items 2–4, built in 0.11.3 item 10; owner:
// «всё унифицировано — нумерация, методология, орфография, архитектура всех
// роадмапов»). project_context and phase_gate report the findings of a live
// ROADMAP; CI checks the tracked documents.
package roadmapstyle

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Template is the one ROADMAP skeleton; projects keep their own phases.
const Template = `# <Проект> — ROADMAP

## Релизы
| Версия | Дата | Статус | Что сделано | Пункты |
|---|---|---|---|---|
| 0.N.M | ГГГГ-ММ-ДД | в проде (main <sha>, откат 0.N.M-1) / заменён … / план / в сборке / откат | … | … |

## Очередь
- **0.N.M «Название» (дата, слова владельца):** пункты с приёмкой.

## Реестр багов
Карточки MCP-NNN / LANE-NNN (issue_list), BUGLOG.md — журнал выпусков.

Методология общая: mcpshka_guide topic=rules и topic=roadmap; AGENTS.md проекта только ссылается на них.
`

// Statuses a release row may start with.
var Statuses = []string{"в проде", "заменён", "заменена", "заменен", "план", "в сборке", "откат", "отозван"}

// Terms: wrong spellings outside quotes, code and links → the one spelling.
var terms = []struct {
	re   *regexp.Regexp
	good string
}{
	{term(`мспишк\p{L}*`), "MCPshka"},
	{term(`лейн\p{L}*`), "Lane"},
	{term(`диаг(?:а|е|у|ом)?`), "DIAG"},
	{term(`face ?id|фейс ?айди`), "Face ID"},
	{term(`гитверс\p{L}*|gitver[sc]e`), "GitVerse"},
	{term(`кодекс\p{L}*`), "Codex"},
	{term(`чатгпт\p{L}*|chatgpt`), "ChatGPT"},
	{term(`github`), "GitHub"},
}

// term matches a whole word in any script (RE2 \b knows only ASCII).
func term(word string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}_])(` + word + `)(?:$|[^\p{L}\p{N}_])`)
}

var (
	fence   = regexp.MustCompile("(?s)```.*?```")
	code    = regexp.MustCompile("`[^`\n]*`")
	quote   = regexp.MustCompile(`«[^»\n]*»|"[^"\n]*"`)
	link    = regexp.MustCompile(`https?://\S+|\S+\.(?:com|ru|net|io)\S*`)
	heading = regexp.MustCompile(`(?m)^#{1,4} +(.+)$`)
	version = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// Finding is one deviation from the contract.
type Finding struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

func (f Finding) String() string { return f.Rule + ": " + f.Detail }

// prose removes what is not the author's own wording: code, quotes, links.
func prose(text string) string {
	for _, re := range []*regexp.Regexp{fence, code, quote, link} {
		text = re.ReplaceAllString(text, " ")
	}
	return text
}

// Spelling checks the terms of any document. A lower-case Latin identifier
// (provider=gitverse) is code, not prose.
func Spelling(text string) []Finding {
	out := []Finding{}
	seen := map[string]bool{}
	text = prose(text)
	for _, t := range terms {
		for _, m := range t.re.FindAllStringSubmatch(text, -1) {
			w := m[1]
			if w == t.good || isLowerASCII(w) || seen[w] {
				continue
			}
			seen[w] = true
			out = append(out, Finding{"spelling", fmt.Sprintf("«%s» → %s", w, t.good)})
		}
	}
	return out
}

func isLowerASCII(w string) bool {
	for _, r := range w {
		if r > 127 {
			return false
		}
	}
	return w == strings.ToLower(w)
}

// Check grades a ROADMAP: required sections, plain 0.N.M versions and known
// statuses in the release table, and spelling. At most limit findings.
func Check(text string, limit int) []Finding {
	out := []Finding{}
	heads := heading.FindAllStringSubmatch(text, -1)
	for _, want := range []string{"Релизы", "Очередь"} {
		found := false
		for _, h := range heads {
			found = found || strings.Contains(h[1], want)
		}
		if !found {
			out = append(out, Finding{"section", "нет раздела «" + want + "» (шаблон: roadmapstyle.Template)"})
		}
	}
	out = append(out, releaseRows(text)...)
	out = append(out, Spelling(text)...)
	if limit > 0 && len(out) > limit {
		out = append(out[:limit], Finding{"more", fmt.Sprintf("ещё %d", len(out)-limit)})
	}
	return out
}

// releaseRows reads the first table whose header starts with «| Версия».
func releaseRows(text string) []Finding {
	out := []Finding{}
	in := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !in {
			in = strings.HasPrefix(line, "| Версия")
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 || strings.HasPrefix(strings.TrimSpace(cells[0]), "---") {
			continue
		}
		ver := strings.Trim(strings.TrimSpace(cells[0]), "*~ ")
		status := strings.ToLower(strings.Trim(strings.TrimSpace(cells[2]), "*~ "))
		if !version.MatchString(ver) {
			out = append(out, Finding{"version", "«" + ver + "»: версии — просто 0.N.M, без rc/dev/тиров"})
		}
		known := false
		for _, s := range Statuses {
			known = known || strings.HasPrefix(status, s)
		}
		if !known {
			out = append(out, Finding{"status", ver + ": статус «" + clip(status, 30) + "» — один из: " + strings.Join(Statuses, ", ")})
		}
	}
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// CheckFile grades root/ROADMAP.md; nil without one.
func CheckFile(root string, limit int) []string {
	raw, err := os.ReadFile(filepath.Join(root, "ROADMAP.md"))
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range Check(string(raw), limit) {
		out = append(out, f.String())
	}
	return out
}
