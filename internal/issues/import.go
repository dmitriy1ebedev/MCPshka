package issues

import (
	"regexp"
	"strings"
)

// The markdown registry has two card headings:
//
//	### MCP-001 · Автономность · P0          (category, priority; title in **Проблема:**)
//	### MCP-112 · P1 · Страница Face ID …    (priority, title)
//
// A card runs until the next heading of level 1-3. A repeated id appends.
var (
	cardHeading  = regexp.MustCompile(`^###\s+((?:MCP|PRJ)-[0-9]{3,5})\s*·\s*(.+)$`)
	anyHeading   = regexp.MustCompile(`^#{1,3}\s`)
	priorityPart = regexp.MustCompile(`^P[0-3]$`)
	statusLine   = regexp.MustCompile(`^\*\*Статус:\*\*\s*(.+)$`)
	problemLine  = regexp.MustCompile(`^\*\*(?:Проблема:\*\*|Суть\.\*\*)\s*(.+)$`)
)

type ImportResult struct {
	Added   []string `json:"added"`
	Skipped []string `json:"skipped,omitempty"`
	Total   int      `json:"total"`
}

type parsedCard struct {
	id, title, priority, category, statusNote string
	body                                      []string
}

func parseRegistry(text string) []parsedCard {
	var cards []parsedCard
	index := map[string]int{}
	cur := -1
	for _, line := range strings.Split(text, "\n") {
		if m := cardHeading.FindStringSubmatch(line); m != nil {
			id := m[1]
			if at, seen := index[id]; seen {
				cur = at
				cards[cur].body = append(cards[cur].body, "", "---", "")
				continue
			}
			c := parsedCard{id: id}
			parts := strings.Split(m[2], "·")
			var rest []string
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if priorityPart.MatchString(p) && c.priority == "" {
					c.priority = p
				} else if p != "" {
					rest = append(rest, p)
				}
			}
			switch {
			case len(rest) == 1 && strings.Index(m[2], c.priority) < strings.Index(m[2], rest[0]):
				c.title = rest[0]
			case len(rest) >= 1:
				c.category = rest[0]
				c.title = strings.Join(rest[1:], " · ")
			}
			index[id] = len(cards)
			cards = append(cards, c)
			cur = len(cards) - 1
			continue
		}
		if cur >= 0 && anyHeading.MatchString(line) {
			cur = -1
			continue
		}
		if cur < 0 {
			continue
		}
		c := &cards[cur]
		if m := statusLine.FindStringSubmatch(line); m != nil && c.statusNote == "" {
			c.statusNote = strings.TrimSpace(m[1])
		}
		if m := problemLine.FindStringSubmatch(line); m != nil && c.title == "" {
			c.title = firstSentence(m[1])
		}
		c.body = append(c.body, line)
	}
	return cards
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	for _, sep := range []string{". ", "; "} {
		if i := strings.Index(s, sep); i > 20 {
			s = s[:i]
			break
		}
	}
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

// Import adds cards from a markdown registry; existing ids are left alone
// so re-importing never overwrites work done in the interface.
func (s *Store) Import(text, source, actor string) (ImportResult, error) {
	cards := parseRegistry(text)
	var out ImportResult
	err := s.change(func(all map[string]*Issue) error {
		now := s.now().UTC()
		for _, c := range cards {
			if _, exists := all[c.id]; exists {
				out.Skipped = append(out.Skipped, c.id)
				continue
			}
			body := strings.TrimSpace(strings.Join(c.body, "\n"))
			if len(body) > maxBody {
				body = body[:maxBody]
			}
			prio := c.priority
			if prio == "" {
				prio = "P2"
			}
			title := c.title
			if title == "" {
				title = c.id
			}
			project := ""
			if strings.HasPrefix(c.id, "PRJ-") {
				project = c.category
			}
			i := &Issue{ID: c.id, Title: title, Priority: prio, Status: Open, Category: c.category, Project: project, Body: body, StatusNote: c.statusNote, Source: source, CreatedAt: now, UpdatedAt: now}
			i.event(now, actor, "import", source)
			all[c.id] = i
			out.Added = append(out.Added, c.id)
		}
		out.Total = len(all)
		return nil
	})
	return out, err
}
