// Package roadmapnext finds the next unfinished item of any project's ROADMAP
// without a project-specific format: DIAG-style "## P03 — title" sections
// with "- [ ]" items and LANE-style numbered "0. **[-] …**" items both work.
package roadmapnext

import (
	"errors"
	"regexp"
	"strings"

	"mcpshka/internal/roadmapscan"
)

// Item is the next phase or item a run should take.
type Item struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Line       int      `json:"line"`
	Open       []string `json:"open"`
	Done       int      `json:"done"`
	Acceptance string   `json:"acceptance,omitempty"`
}

var (
	heading = regexp.MustCompile(`^(#{2,4})\s+(.+?)\s*$`)

	phaseID = regexp.MustCompile(`\b(P\d{1,3}[a-z]?)\b`)
	accept  = regexp.MustCompile(`^\s*\*\*(?:DONE|Done|Acceptance|Приёмка|Готово)[^*]*\*\*\s*(.*)$`)
)

// Next returns the first section that still has an open ("[ ]" or "[-]")
// item. Sections without checkboxes are ignored; an item whose whole section
// is checked is done.
func Next(roadmap string) (Item, error) {
	type section struct {
		item  Item
		level int
	}
	var cur *section
	var found *Item
	finish := func() {
		if cur != nil && found == nil && len(cur.item.Open) > 0 {
			it := cur.item
			found = &it
		}
	}
	lines, _ := roadmapscan.ContentLines(roadmap)
	for i, line := range lines {
		if m := heading.FindStringSubmatch(line); m != nil {
			if cur != nil && cur.level > 0 && len(m[1]) > cur.level {
				continue
			}
			finish()
			if found != nil {
				break
			}
			title := strings.Trim(m[2], "*` ")
			id := roadmapscan.SectionID(line)
			level := 0
			if id != "" {
				level = len(m[1])
			}
			if id == "" {
				id = title
			}
			if p := phaseID.FindString(title); id == title && p != "" {
				id = p
			}
			cur = &section{item: Item{ID: id, Title: title, Line: i + 1}, level: level}
			continue
		}
		if cur == nil {
			continue
		}
		if m := roadmapscan.Checkbox.FindStringSubmatch(line); m != nil {
			text := strings.TrimSpace(strings.Trim(m[4], "* "))
			if len(text) > 300 {
				text = text[:300] + "…"
			}
			if m[2] == "x" || m[2] == "X" {
				cur.item.Done++
			} else if roadmapscan.Superseded(m[2], m[4]) == "" {
				cur.item.Open = append(cur.item.Open, text)
			}
			continue
		}
		if m := accept.FindStringSubmatch(line); m != nil && cur.item.Acceptance == "" {
			cur.item.Acceptance = strings.TrimSpace(m[1])
		}
	}
	finish()
	if found == nil {
		return Item{}, errors.New("roadmap has no open items")
	}
	if found.ID == found.Title && !phaseID.MatchString(found.ID) && len(found.Open) > 0 {
		// Numbered queues name the item by its first open entry.
		found.ID = strings.Fields(found.Open[0] + " item")[0]
		if id := roadmapscan.EntryID(found.Open[0]); id != "" {
			found.ID = id
		} else if p := phaseID.FindString(found.Open[0]); p != "" {
			found.ID = p
		}
	}
	return *found, nil
}
