package roadmapscan

import (
	"regexp"
	"strings"
)

var (
	primaryHeading  = regexp.MustCompile(`^## (P[0-9]{1,2})(?: [—-]|:) (.+)$`)
	streamHeading   = regexp.MustCompile(`^#{2,3} (?:Приоритет |Фаза |Phase )?(MT|[A-OQ-Z][A-Z]{0,2}[0-9]{1,2}[a-z]?|P[A-Z]{1,2}[0-9]{1,2}[a-z]?)(?: [—-]|:) (.+)$`)
	markdownHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	Checkbox        = regexp.MustCompile(`^(\s*(?:[-*+]|\d+\.)\s*\**\s*)\[( |-|x|X|~)\](\s*\**\s*)(.*)$`)
	itemID          = regexp.MustCompile(`^(?:\([^()]*\)\s+)?([A-Z]{1,4}[0-9]{1,3}[a-z]?(?:\.[A-Z0-9]+[a-z]?)*|MT(?:\.[A-Z0-9]+[a-z]?)+)[.:)]?(?:[\s*]|$)`)
)

func HeadingLevel(line string) int {
	if m := markdownHeading.FindStringSubmatch(line); m != nil {
		return len(m[1])
	}
	return 0
}

func SectionID(line string) string {
	if h, _ := sectionHeading(line); h != nil {
		return h[1]
	}
	return ""
}

func sectionHeading(line string) ([]string, bool) {
	if m := primaryHeading.FindStringSubmatch(line); m != nil {
		return m, true
	}
	return streamHeading.FindStringSubmatch(line), false
}

func EntryID(text string) string {
	if m := itemID.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// ContentLines preserves line numbers and masks fenced Markdown examples.
func ContentLines(content string) ([]string, bool) {
	lines := strings.Split(content, "\n")
	fence := ""
	for i, raw := range lines {
		line := strings.TrimSuffix(raw, "\r")
		marker := strings.TrimLeft(line, " ")
		switch {
		case fence != "":
			if strings.HasPrefix(marker, fence) && strings.Trim(strings.TrimSpace(marker), fence[:1]) == "" {
				fence = ""
			}
			lines[i] = ""
		case strings.HasPrefix(marker, "```"), strings.HasPrefix(marker, "~~~"):
			fence = marker[:len(marker)-len(strings.TrimLeft(marker, marker[:1]))]
			lines[i] = ""
		default:
			lines[i] = line
		}
	}
	return lines, fence != ""
}

var decisionRef = regexp.MustCompile(`^(?:(?:knowledge|memory):m-[0-9a-f]{12}|commit:[0-9a-f]{40})$`)
var supersededComment = regexp.MustCompile(`<!-- superseded: (\S+) -->\s*$`)

func ValidDecision(ref string) bool { return decisionRef.MatchString(ref) }

func Superseded(mark, text string) string {
	if mark == "~" {
		if m := supersededComment.FindStringSubmatch(text); m != nil && ValidDecision(m[1]) {
			return m[1]
		}
	}
	return ""
}
