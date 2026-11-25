package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Memory audit and optimize (ROADMAP S4.K2): read-only findings, a plan
// pinned by its digest, an applied plan journaled in mem_ops and undone by
// its op id. Nothing is deleted: stale records become archive or are closed
// as superseded, their history stays (as_of).

// LogStaleAfter: a checkpoint/journal section older than this stops being
// current even when it is the newest one imported (the import is manual).
const LogStaleAfter = 7 * 24 * time.Hour

type AuditOp struct {
	ID     string `json:"id"`
	Op     string `json:"op"` // archive | close
	By     string `json:"by,omitempty"`
	Reason string `json:"reason"`
	Title  string `json:"title"`
}

type AuditReport struct {
	Current    int            `json:"current"`
	ByKind     map[string]int `json:"by_kind"`
	StaleLogs  int            `json:"stale_logs"`
	Duplicates int            `json:"duplicates"`
	OldRelease int            `json:"old_releases"`
	NoSource   int            `json:"no_source"`
	Plan       []AuditOp      `json:"plan"`
	PlanSHA    string         `json:"plan_sha,omitempty"`
	Control    ControlReport  `json:"control"`
	Next       string         `json:"next,omitempty"`
}

type auditRec struct {
	id, kind, title, body, source string
	from                          time.Time
}

var (
	logSection = regexp.MustCompile(`^(claude-memory:[^#]*(?:checkpoint|log)[^#]*)#(\d+)`)
	inProd     = regexp.MustCompile(`(?i)^(.{2,40}?)\s+v?(\d+)\.(\d+)\.(\d+)\b.*в проде`)
	spaces     = regexp.MustCompile(`\s+`)
)

func (s *Store) current(ctx context.Context) ([]auditRec, error) {
	rows, err := s.read(ctx, "SELECT id, kind, title, body, coalesce(source,'') AS source, valid_from FROM mem WHERE origin='own' AND valid_to IS NULL;")
	if err != nil {
		return nil, err
	}
	out := make([]auditRec, 0, len(rows))
	for _, m := range rows {
		r := auditRec{id: str(m, "id"), kind: str(m, "kind"), title: str(m, "title"), body: str(m, "body"), source: str(m, "source")}
		if t := parseTime(m, "valid_from"); t != nil {
			r.from = *t
		}
		out = append(out, r)
	}
	return out, nil
}

// Audit finds stale log sections, duplicate bodies and superseded
// "X a.b.c в проде" facts among the current own records.
func (s *Store) Audit(ctx context.Context) (AuditReport, error) {
	recs, err := s.current(ctx)
	if err != nil {
		return AuditReport{}, err
	}
	rep := AuditReport{Current: len(recs), ByKind: map[string]int{}}
	now := s.now().UTC()
	planned := map[string]bool{}
	add := func(op AuditOp) {
		if !planned[op.ID] {
			planned[op.ID] = true
			rep.Plan = append(rep.Plan, op)
		}
	}
	newest := map[string]int{}
	for _, r := range recs {
		rep.ByKind[r.kind]++
		if r.source == "" {
			rep.NoSource++
		}
		if m := logSection.FindStringSubmatch(r.source); m != nil {
			n, _ := strconv.Atoi(m[2])
			newest[m[1]] = max(newest[m[1]], n)
		}
	}
	for _, r := range recs {
		m := logSection.FindStringSubmatch(r.source)
		if m == nil || r.kind == "archive" {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if n < newest[m[1]] || now.Sub(r.from) > LogStaleAfter {
			rep.StaleLogs++
			add(AuditOp{ID: r.id, Op: "archive", Reason: "старый раздел журнала " + m[1], Title: r.title})
		}
	}
	keep := func(group []auditRec, reason string, count *int) {
		if len(group) < 2 {
			return
		}
		for _, r := range group[1:] {
			*count++
			add(AuditOp{ID: r.id, Op: "close", By: group[0].id, Reason: reason, Title: r.title})
		}
	}
	bodies := map[string][]auditRec{}
	for _, r := range recs {
		if b := strings.ToLower(spaces.ReplaceAllString(strings.TrimSpace(r.body), " ")); len(b) >= 40 {
			bodies[b] = append(bodies[b], r)
		}
	}
	for _, g := range sortedGroups(bodies, func(a, b auditRec) bool { return a.from.After(b.from) }) {
		keep(g, "дубль текста", &rep.Duplicates)
	}
	releases := map[string][]auditRec{}
	version := map[string]int{}
	for _, r := range recs {
		if m := inProd.FindStringSubmatch(r.title); m != nil && r.kind == "fact" {
			a, _ := strconv.Atoi(m[2])
			b, _ := strconv.Atoi(m[3])
			c, _ := strconv.Atoi(m[4])
			version[r.id] = a*1e6 + b*1e3 + c
			name := strings.ToLower(strings.TrimSpace(m[1]))
			releases[name] = append(releases[name], r)
		}
	}
	for _, g := range sortedGroups(releases, func(a, b auditRec) bool { return version[a.id] > version[b.id] }) {
		keep(g, "есть выпуск новее", &rep.OldRelease)
	}
	sort.Slice(rep.Plan, func(i, j int) bool { return rep.Plan[i].ID < rep.Plan[j].ID })
	if rep.Control, err = s.control(ctx, planned); err != nil {
		return rep, err
	}
	if len(rep.Plan) > 0 {
		rep.PlanSHA = planSHA(rep.Plan)
		rep.Next = "memory_write optimize=<plan_sha> admin_session_id=… применяет план; отмена — optimize_undo=<op>"
	}
	return rep, nil
}

func sortedGroups(m map[string][]auditRec, less func(a, b auditRec) bool) [][]auditRec {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]auditRec, 0, len(keys))
	for _, k := range keys {
		g := m[k]
		sort.SliceStable(g, func(i, j int) bool { return less(g[i], g[j]) })
		out = append(out, g)
	}
	return out
}

func planSHA(plan []AuditOp) string {
	h := sha256.New()
	for _, p := range plan {
		fmt.Fprintf(h, "%s\t%s\t%s\n", p.ID, p.Op, p.By)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// OptimizeResult is one applied (or undone) plan.
type OptimizeResult struct {
	Op      string `json:"op"`
	Applied int    `json:"applied"`
	Undone  bool   `json:"undone,omitempty"`
}

const opsTable = "CREATE TABLE IF NOT EXISTS mem_ops(op TEXT NOT NULL, at TEXT NOT NULL, id TEXT NOT NULL, kind TEXT, valid_to TEXT, superseded_by TEXT);\n"

// Optimize applies the audit plan whose digest the caller saw; a plan that
// changed since is refused, so nothing unreviewed is applied.
func (s *Store) Optimize(ctx context.Context, sha string) (OptimizeResult, error) {
	rep, err := s.Audit(ctx)
	if err != nil {
		return OptimizeResult{}, err
	}
	if len(rep.Plan) == 0 || rep.PlanSHA != sha {
		return OptimizeResult{}, fmt.Errorf("%w: plan changed or empty; run memory_recall audit=true again", ErrInvalid)
	}
	op := sha[:12]
	now := s.now().UTC()
	var b strings.Builder
	b.WriteString("BEGIN IMMEDIATE;\n" + opsTable)
	for _, p := range rep.Plan {
		fmt.Fprintf(&b, "INSERT INTO mem_ops SELECT %s, %s, id, kind, valid_to, superseded_by FROM mem WHERE id=%s;\n", lit(op), litTime(&now), lit(p.ID))
		if p.Op == "archive" {
			fmt.Fprintf(&b, "UPDATE mem SET kind='archive' WHERE id=%s;\n", lit(p.ID))
		} else {
			fmt.Fprintf(&b, "UPDATE mem SET valid_to=%s, superseded_by=%s WHERE id=%s AND valid_to IS NULL;\n", litTime(&now), lit(p.By), lit(p.ID))
		}
	}
	b.WriteString(invalidateSummariesSQL() + "COMMIT;")
	if _, err := s.execFor(ctx, 30*time.Second, b.String()); err != nil {
		return OptimizeResult{}, err
	}
	return OptimizeResult{Op: op, Applied: len(rep.Plan)}, nil
}

// Undo restores the records of one applied plan exactly as they were.
func (s *Store) Undo(ctx context.Context, op string) (OptimizeResult, error) {
	o := lit(op)
	sel := func(col string) string {
		return "(SELECT " + col + " FROM mem_ops o WHERE o.op=" + o + " AND o.id=mem.id)"
	}
	rows, err := s.execFor(ctx, 30*time.Second, "BEGIN IMMEDIATE;\n"+opsTable+
		"UPDATE mem SET kind="+sel("kind")+", valid_to="+sel("valid_to")+", superseded_by="+sel("superseded_by")+
		" WHERE id IN (SELECT id FROM mem_ops WHERE op="+o+");\nSELECT changes() AS n;\n"+
		"DELETE FROM mem_ops WHERE op="+o+";\n"+invalidateSummariesSQL()+"COMMIT;")
	if err != nil {
		return OptimizeResult{}, err
	}
	n := 0
	if len(rows) == 1 {
		f, _ := rows[0]["n"].(float64)
		n = int(f)
	}
	if n == 0 {
		return OptimizeResult{}, fmt.Errorf("%w: no applied plan %s", ErrInvalid, op)
	}
	return OptimizeResult{Op: op, Applied: n, Undone: true}, nil
}
