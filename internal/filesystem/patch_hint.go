package filesystem

import (
	"bytes"
	"fmt"
	"strings"
)

// patchMiss explains a replacement count mismatch the way Desktop Commander
// does for edit_block: where the nearest text is and what differs, so the
// next call can be exact instead of a blind re-read (owner 24.11: «впитать
// лучшие идеи Remote Desktop Commander»).
func patchMiss(data []byte, oldText string, found, expected int) error {
	base := fmt.Sprintf("replacement count mismatch: found=%d expected=%d", found, expected)
	if found > 0 {
		return fmt.Errorf("%s; old_text starts at lines %s — add surrounding lines to make it unique or set expected_replacements", base, matchLines(data, oldText, 5))
	}
	if hint := nearest(string(data), oldText); hint != "" {
		return fmt.Errorf("%s; %s", base, hint)
	}
	return fmt.Errorf("%s; no similar text in the file: re-read it (fs_read) before patching", base)
}

func matchLines(data []byte, oldText string, limit int) string {
	var at []string
	for off := 0; len(at) < limit; {
		i := bytes.Index(data[off:], []byte(oldText))
		if i < 0 {
			break
		}
		off += i
		at = append(at, fmt.Sprint(bytes.Count(data[:off], []byte("\n"))+1))
		off++
	}
	return strings.Join(at, ", ")
}

// nearest finds the window of lines most like old_text and names the first
// line that differs. Whitespace-only and line-ending differences are named
// as such: they are the usual near misses.
func nearest(text, oldText string) string {
	if strings.Contains(text, "\r\n") && !strings.Contains(oldText, "\r\n") &&
		strings.Contains(strings.ReplaceAll(text, "\r\n", "\n"), oldText) {
		return "the file uses CRLF line endings and old_text LF: send \\r\\n"
	}
	lines := strings.Split(text, "\n")
	want := strings.Split(strings.TrimSuffix(oldText, "\n"), "\n")
	if len(want) == 0 || len(lines) == 0 || len(want) > 400 {
		return ""
	}
	bestAt, bestScore := -1, 0
	for i := 0; i+len(want) <= len(lines) || (i == 0 && len(lines) < len(want)); i++ {
		score := 0
		for k := 0; k < len(want) && i+k < len(lines); k++ {
			switch {
			case lines[i+k] == want[k]:
				score += 2
			case strings.TrimSpace(lines[i+k]) == strings.TrimSpace(want[k]) && want[k] != "":
				score++
			}
		}
		if score > bestScore {
			bestAt, bestScore = i, score
		}
		if i+len(want) > len(lines) {
			break
		}
	}
	if bestAt < 0 || bestScore*4 < len(want)*2 { // under a quarter of the lines alike
		return ""
	}
	same, spaceOnly := 0, true
	first := -1
	for k := 0; k < len(want) && bestAt+k < len(lines); k++ {
		got := lines[bestAt+k]
		if got == want[k] {
			same++
			continue
		}
		if strings.TrimSpace(got) != strings.TrimSpace(want[k]) {
			spaceOnly = false
		}
		if first < 0 {
			first = k
		}
	}
	if first < 0 {
		first = len(want) - 1
	}
	kind := "text"
	if spaceOnly {
		kind = "whitespace/indentation only"
	}
	got := ""
	if bestAt+first < len(lines) {
		got = lines[bestAt+first]
	}
	return fmt.Sprintf("closest text at line %d (%d/%d lines identical, differs in %s); first difference at line %d: file has %q, old_text has %q",
		bestAt+1, same, len(want), kind, bestAt+first+1, clip(got), clip(want[first]))
}

func clip(s string) string {
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
