package workmetrics

import (
	"context"
	"time"

	"mcpshka/internal/audit"
)

const SessionCall = "autonomy_tool_call"

type call struct {
	at      time.Time
	session string
	project string
	mission string
	tool    string
}

type callIndex struct {
	bySession map[string][]call
	missions  map[string][]call
	mutations map[string]string
	sessions  int
	resumes   int
	noEffect  int
	complete  bool
}

func (a *Autonomy) readAudit(ctx context.Context, l *audit.Logger) callIndex {
	x := callIndex{bySession: map[string][]call{}, missions: map[string][]call{}, mutations: map[string]string{}}
	if l == nil {
		a.Notes = append(a.Notes, "session and mutation journal unavailable")
		return x
	}
	approvals := map[string]bool{}
	noEffect := map[string]bool{}
	earliest := time.Time{}
	complete, err := l.Recent(ctx, a.Since, func(e audit.Event) {
		if !a.contains(e.Time) {
			return
		}
		switch e.Type {
		case SessionCall:
			session, _ := e.Details["session"].(string)
			id, _ := e.Details["mission"].(string)
			approval, _ := e.Details["approval"].(string)
			if session == "" {
				return
			}
			if earliest.IsZero() || e.Time.Before(earliest) {
				earliest = e.Time
			}
			c := call{at: e.Time, session: session, project: e.Workdir, mission: id, tool: e.Command}
			x.bySession[session] = append(x.bySession[session], c)
			if c.mission != "" && c.tool == "mission_start" {
				x.missions[c.mission] = append(x.missions[c.mission], c)
			}
			if a.project(c.project) {
				x.sessions++
				if c.tool == "mission_resume" && e.Result == "ok" {
					x.resumes++
				}
				if approval != "" {
					approvals[approval] = true
				}
			}
		case "mutation_contract":
			key, _ := e.Details["receipt_key_sha256"].(string)
			if key != "" && e.Workdir != "" {
				x.mutations[key] = e.Workdir
			}
			if key != "" && a.project(e.Workdir) && e.Result == "rejected_no_effect" {
				noEffect[key] = true
			}
		}
	})
	x.noEffect = len(noEffect)
	x.complete = complete && err == nil
	if !x.complete {
		a.Notes = append(a.Notes, "audit and session journal scan incomplete")
	}
	if x.sessions > 0 {
		n := len(approvals)
		a.FaceIDRequests = &n
		a.Notes = append(a.Notes, "Face ID counts distinct requests observed in session tool replies; unattributed requests are excluded by a project filter")
	}
	if earliest.IsZero() || earliest.After(a.Since) {
		a.Notes = append(a.Notes, "session call history does not establish coverage for the entire period")
	}
	return x
}

func (x callIndex) forMission(id string, start, done time.Time) (int, bool) {
	m := x.missions[id]
	if !x.complete || len(m) != 1 {
		return 0, false
	}
	n := 0
	for _, c := range x.bySession[m[0].session] {
		if c.at.Before(m[0].at) || c.at.After(done) {
			continue
		}
		// An overlapping mission or another explicit project makes the
		// session window ambiguous.
		if c.tool == "mission_start" && c.mission != id || (c.mission != "" && c.mission != id) ||
			(c.project != "" && m[0].project != "" && c.project != m[0].project) {
			return 0, false
		}
		n++
	}
	return n, n > 0 && !m[0].at.After(start)
}
