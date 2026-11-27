// Package perfbudget compares `go test -bench` output of a candidate against
// a baseline run on the same machine (MCP-052). A refactor without a
// measured result does not pass: every benchmark must stay within the time
// budget and must not allocate more per op.
package perfbudget

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Result struct {
	Name     string  `json:"name"`
	NsPerOp  float64 `json:"ns_per_op"`
	AllocsOp float64 `json:"allocs_per_op"`
	Samples  int     `json:"samples"`
}

type Delta struct {
	Name        string  `json:"name"`
	BaseNs      float64 `json:"base_ns"`
	CandNs      float64 `json:"cand_ns"`
	TimePercent float64 `json:"time_delta_percent"`
	BaseAllocs  float64 `json:"base_allocs"`
	CandAllocs  float64 `json:"cand_allocs"`
	Verdict     string  `json:"verdict"` // OK, IMPROVED, REGRESSED, MISSING, NEW
}

var benchLine = regexp.MustCompile(`^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([0-9.]+) ns/op(?:.*?\s([0-9.]+) allocs/op)?`)

// Parse averages repeated runs (-count=N) of each benchmark (median is
// approximated by the mean of the samples, which -count=5 keeps stable).
func Parse(r io.Reader) (map[string]Result, error) {
	out := map[string]Result{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		m := benchLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		ns, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			return nil, err
		}
		allocs := 0.0
		if m[3] != "" {
			allocs, _ = strconv.ParseFloat(m[3], 64)
		}
		cur := out[m[1]]
		cur.Name = m[1]
		cur.NsPerOp = (cur.NsPerOp*float64(cur.Samples) + ns) / float64(cur.Samples+1)
		cur.AllocsOp = max(cur.AllocsOp, allocs)
		cur.Samples++
		out[m[1]] = cur
	}
	return out, sc.Err()
}

// Compare fails when any benchmark is slower than maxRegress percent, when it
// allocates more per op, or when a baseline benchmark disappeared.
func Compare(base, cand map[string]Result, maxRegress float64) ([]Delta, error) {
	names := map[string]bool{}
	for n := range base {
		names[n] = true
	}
	for n := range cand {
		names[n] = true
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var out []Delta
	var bad []string
	for _, n := range sorted {
		b, okb := base[n]
		c, okc := cand[n]
		d := Delta{Name: n, BaseNs: b.NsPerOp, CandNs: c.NsPerOp, BaseAllocs: b.AllocsOp, CandAllocs: c.AllocsOp}
		switch {
		case !okc:
			d.Verdict = "MISSING"
			bad = append(bad, n+" missing")
		case !okb:
			d.Verdict = "NEW"
		default:
			if b.NsPerOp > 0 {
				d.TimePercent = (c.NsPerOp - b.NsPerOp) / b.NsPerOp * 100
			}
			d.Verdict = "OK"
			if d.TimePercent < -maxRegress {
				d.Verdict = "IMPROVED"
			}
			if d.TimePercent > maxRegress || c.AllocsOp > b.AllocsOp {
				d.Verdict = "REGRESSED"
				bad = append(bad, fmt.Sprintf("%s %+.1f%% time, allocs %.0f->%.0f", n, d.TimePercent, b.AllocsOp, c.AllocsOp))
			}
		}
		out = append(out, d)
	}
	if len(bad) > 0 {
		return out, fmt.Errorf("PERF_BUDGET_EXCEEDED (max +%.0f%%): %s", maxRegress, strings.Join(bad, "; "))
	}
	return out, nil
}
