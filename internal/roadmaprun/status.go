package roadmaprun

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"
)

func ReadJournal(path string, boot time.Time) (Journal, error) {
	var j Journal
	f, err := os.Open(path)
	if err != nil {
		return j, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return j, err
	}
	if len(raw) > 1<<20 {
		return j, errors.New("journal exceeds 1 MiB")
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return j, err
	}
	j = MarkInterrupted(j, boot)
	if j.Goal == "" {
		j.Goal = "ROADMAP: " + j.Roadmap
	}
	if len(j.Phases) > 0 {
		ph := j.Phases[len(j.Phases)-1]
		if j.CurrentPhase == "" {
			j.CurrentPhase = ph.ID
		}
		if j.Agent == "" {
			j.Agent = ph.Agent
		}
		if j.LastGate == "" {
			j.LastGate = ph.Verdict
		}
	}
	if j.Step == "" {
		j.Step = "unknown"
	}
	if j.State == "interrupted" {
		j.Blocker = j.StopReason
		j.NextAction = "reconcile project_context, project_lease_status and recorded jobs; preserve goal and dirty work before resubmitting"
	}
	return j, nil
}

// Recent keeps active runs even when newer finished runs fill the history.
// Bounds are explicit; truncated/invalid evidence is never a clean bill.
func Recent(path string, boot time.Time) ([]Journal, bool, error) {
	dir, err := os.Open(stateDir)
	if os.IsNotExist(err) {
		return []Journal{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(1001)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	truncated := len(names) > 1000
	if truncated {
		names = names[:1000]
	}
	var runs []Journal
	for _, name := range names {
		if filepath.Ext(name) != ".json" {
			continue
		}
		j, err := ReadJournal(filepath.Join(stateDir, name), boot)
		if err != nil {
			truncated = true
			continue
		}
		if path != "" && j.Path != filepath.Clean(path) {
			continue
		}
		j.Phases = nil
		if j.Blocker == "" {
			j.Blocker = j.StopReason
		}
		j.Blocker = readableBlocker(j.Blocker)
		j.StopReason = ""
		compact := func(value string, limit int) string {
			if len(value) > limit {
				j.DetailTruncated = true
				value = value[:limit]
				for !utf8.ValidString(value) {
					value = value[:len(value)-1]
				}
			}
			return value
		}
		j.Goal, j.Blocker, j.NextAction, j.LastGate = compact(j.Goal, 384), compact(j.Blocker, 384), compact(j.NextAction, 256), compact(j.LastGate, 128)
		runs = append(runs, j)
	}
	sort.Slice(runs, func(i, j int) bool {
		if (runs[i].State == "running") != (runs[j].State == "running") {
			return runs[i].State == "running"
		}
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	markSuperseded(runs)
	recent := 0
	out := []Journal{}
	for _, j := range runs {
		if j.State != "running" {
			recent++
			if recent > 20 {
				continue
			}
		}
		if len(out) >= 50 {
			truncated = true
			break
		}
		out = append(out, j)
	}
	return out, truncated, nil
}

// markSuperseded names, for every stopped run, the newest later run on the
// same path. Its blocker stays as history; next_action stops looking like
// open work unless the run still asks for reconciliation of its own lease
// or jobs (ChatGPT audit 03.11.2026 counted old stopped runs as open).
func markSuperseded(runs []Journal) {
	newest := map[string]Journal{}
	for i := range runs {
		j := &runs[i]
		if n, ok := newest[j.Path]; ok && j.State != "running" && n.StartedAt.After(j.StartedAt) {
			j.SupersededBy = n.ID
			if j.Step != "reconciliation_required" {
				j.NextAction = "none: superseded by " + n.ID
			}
		}
		if n, ok := newest[j.Path]; !ok || j.StartedAt.After(n.StartedAt) {
			newest[j.Path] = *j
		}
	}
}
