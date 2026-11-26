package roadmapscan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const maxFindings = 12

// Analysis is roadmap progress in numbers plus the places where the document
// contradicts itself. UNTRUSTED like the roadmap: it points at stale
// acceptance evidence, it accepts nothing.
type Analysis struct {
	Phases           int `json:"phases"`
	PhasesDone       int `json:"phases_done"`
	Checks           int `json:"checks"`
	ChecksSuperseded int `json:"checks_superseded,omitempty"`
	ChecksDone       int `json:"checks_done"`
	Percent          int `json:"percent"`
	// Inconsistent: declared DONE with open items, every item done without a
	// DONE mark, no acceptance items at all, a status table that disagrees
	// with the sections. At most 12; More counts the rest.
	Inconsistent []string `json:"inconsistent,omitempty"`
	More         int      `json:"more_inconsistent,omitempty"`
}

// "| P4 — Title | 1 | 0 | 16 | status |": done and open counts of a phase as
// the status table claims them.
var statusRow = regexp.MustCompile(`^\| (P[0-9]{1,2}) [—-] [^|]*\|[^|]*\| ([0-9]+) \| ([0-9]+) \|`)

// Analyze loads the roadmap like Load and measures it.
func Analyze(path, expectedSHA256 string) (Snapshot, Analysis, error) {
	snap, content, err := load(path, expectedSHA256)
	if err != nil {
		return snap, Analysis{}, err
	}
	return snap, analyze(snap, content), nil
}

func analyze(snap Snapshot, content string) Analysis {
	a := Analysis{Phases: len(snap.Phases)}
	note := func(format string, args ...any) {
		if len(a.Inconsistent) < maxFindings {
			a.Inconsistent = append(a.Inconsistent, fmt.Sprintf(format, args...))
		} else {
			a.More++
		}
	}
	byID := make(map[string]Phase, len(snap.Phases))
	for _, p := range snap.Phases {
		byID[p.ID] = p
		a.Checks += p.Total
		a.ChecksDone += p.Completed
		a.ChecksSuperseded += p.Superseded
		if p.DeclaredDone {
			a.PhasesDone++
		}
		switch open := p.Total - p.Completed - p.Superseded; {
		case p.DeclaredDone && open > 0:
			note("%s: declared DONE with %d open items", p.ID, open)
		case !p.DeclaredDone && p.Total > 0 && open == 0:
			if p.Superseded > 0 {
				note("%s: no open items (%d superseded); the phase is not marked DONE", p.ID, p.Superseded)
			} else {
				note("%s: all %d items done but the phase is not marked DONE (acceptance missing)", p.ID, p.Total)
			}
		case !p.DeclaredDone && !p.Deferred && p.Total == 0:
			note("%s: no acceptance items", p.ID)
		}
	}
	if a.Checks > 0 {
		a.Percent = a.ChecksDone * 100 / a.Checks
	}
	for _, line := range strings.Split(content, "\n") {
		m := statusRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		p, ok := byID[m[1]]
		done, _ := strconv.Atoi(m[2])
		open, _ := strconv.Atoi(m[3])
		if ok && (done != p.Completed || open != p.Total-p.Completed-p.Superseded) {
			note("%s: status table says %d done / %d open, the section has %d / %d", p.ID, done, open, p.Completed, p.Total-p.Completed)
		}
	}
	return a
}
