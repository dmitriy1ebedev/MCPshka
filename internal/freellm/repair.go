package freellm

import "strings"

// repairOld rescues the commonest free-model miss (MCP-239): the old text is
// right but its indentation is not (spaces for tabs, trimmed or shifted
// lines, CRLF). Lines are compared without surrounding whitespace; only a
// window that matches exactly once in the file is accepted, and then old
// becomes the file's own bytes and new is re-indented the same way.
func repairOld(text, old, replacement string) (string, string, bool) {
	want := trimmedLines(old)
	if len(want) == 0 || (len(want) == 1 && len(want[0]) < 24) {
		return "", "", false // too little to anchor on
	}
	lines := strings.Split(text, "\n")
	at := -1
	for i := 0; i+len(want) <= len(lines); i++ {
		ok := true
		for k, w := range want {
			if strings.TrimSpace(lines[i+k]) != w {
				ok = false
				break
			}
		}
		if ok {
			if at >= 0 {
				return "", "", false // ambiguous
			}
			at = i
		}
	}
	if at < 0 {
		return "", "", false
	}
	exact := strings.Join(lines[at:at+len(want)], "\n")
	modelIndent := indentOf(firstNonEmpty(old))
	fileIndent := indentOf(lines[at])
	repl := strings.Split(strings.Trim(strings.ReplaceAll(replacement, "\r\n", "\n"), "\n"), "\n")
	for i, l := range repl {
		if strings.TrimSpace(l) == "" {
			repl[i] = ""
			continue
		}
		rest := strings.TrimPrefix(l, modelIndent)
		if strings.Contains(fileIndent, "\t") {
			// the remaining relative indent in the file's style
			lead := len(rest) - len(strings.TrimLeft(rest, " \t"))
			rest = strings.Repeat("\t", strings.Count(rest[:lead], "\t")+strings.Count(rest[:lead], " ")/4) + rest[lead:]
		}
		repl[i] = fileIndent + rest
	}
	return exact, strings.Join(repl, "\n"), true
}

func trimmedLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		out = append(out, strings.TrimSpace(l))
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func firstNonEmpty(s string) string {
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

func indentOf(l string) string { return l[:len(l)-len(strings.TrimLeft(l, " \t"))] }
