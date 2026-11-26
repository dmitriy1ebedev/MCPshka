package roadmapnext

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"mcpshka/internal/roadmapscan"
)

// ErrPhaseMoved means the phase heading is no longer where it was selected;
// the caller must reconcile instead of guessing which section to close.
var ErrPhaseMoved = errors.New("roadmap phase heading not found")

// MarkDone closes every open ("[ ]"/"[-]") checkbox of the section headed by
// it.Title and nothing else. The section is located by its exact heading
// title, so edits above it between selection and acceptance are harmless.
func MarkDone(roadmap string, it Item) (string, int, error) {
	lines := strings.SplitAfter(roadmap, "\n")
	bodies, _ := roadmapscan.ContentLines(roadmap)
	in, found, flipped, level := false, false, 0, 0
	for i, line := range lines {
		body := bodies[i]
		if m := heading.FindStringSubmatch(body); m != nil {
			if in {
				if level > 0 && len(m[1]) > level {
					continue
				}
				break
			}
			if strings.Trim(m[2], "*` ") == it.Title {
				in, found = true, true
				if roadmapscan.SectionID(body) != "" {
					level = len(m[1])
				}
			}
			continue
		}
		if !in {
			continue
		}
		if m := roadmapscan.Checkbox.FindStringSubmatch(body); m != nil && (m[2] == " " || m[2] == "-" || m[2] == "~" && roadmapscan.Superseded(m[2], m[4]) == "") {
			idx := len(m[1])
			lines[i] = body[:idx] + "[x]" + body[idx+3:] + line[len(body):]
			flipped++
		}
	}
	if !found {
		return roadmap, 0, ErrPhaseMoved
	}
	return strings.Join(lines, ""), flipped, nil
}

// MarkDoneFile applies MarkDone to path with compare-and-swap: the file is
// replaced atomically only if its content is still the bytes MarkDone read.
func MarkDoneFile(path string, it Item) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	out, n, err := MarkDone(string(raw), it)
	if err != nil || n == 0 {
		return 0, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".roadmap-mark-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(out); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), st.Mode().Perm())
	}
	if err != nil {
		return 0, err
	}
	again, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if sha256.Sum256(again) != sha256.Sum256(raw) {
		return 0, errors.New("ROADMAP changed concurrently; CAS refused")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return 0, err
	}
	return n, nil
}
