package issues

// Night review (ROADMAP P29.N1, N3): once a night, while the home is quiet,
// every active card is checked against live facts — no agent, no mutation
// beyond the card itself. A card gets a line only when its verdict changes,
// and the morning brief says what needs the owner.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/runoutcome"
)

// Verdicts, in brief order.
const (
	NightGone         = "gone"          // sentinel symptom absent now
	NightStill        = "still"         // sentinel symptom present now
	NightAwaitAccept  = "await_accept"  // fix is in production, acceptance did not pass
	NightAwaitRelease = "await_release" // fix committed, not released yet
	NightAgentDone    = "agent_done"    // linked agent finished, card not fixed
	NightStale        = "stale"         // nothing moved for a week
	NightOpen         = "open"
)

var nightLabel = map[string]string{
	NightGone: "симптом исчез", NightStill: "симптом держится", NightAwaitAccept: "в проде, ждёт приёмки",
	NightAwaitRelease: "исправлено, ждёт выпуска", NightAgentDone: "агент закончил — проверить ветку",
	NightStale: "неделю без движения", NightOpen: "открыта",
}

var nightOrder = []string{NightGone, NightAwaitAccept, NightAgentDone, NightStill, NightAwaitRelease, NightStale, NightOpen}

// NightInput is what the review sees of the live host.
type NightInput struct {
	Autonomy string
	Now      time.Time
	Findings []Finding                // this pass of host_triage, any severity
	Prod     string                   // installed MCPshka version
	Agent    func(task string) string // agent task state, "" unknown
}

type NightCard struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Since   string `json:"since,omitempty"` // first night with this verdict
}

type NightReport struct {
	runoutcome.Result
	Autonomy string      `json:"autonomy,omitempty"`
	At       time.Time   `json:"at"`
	Prod     string      `json:"prod"`
	Cards    []NightCard `json:"cards"`
}

// versionLess compares 0.N.M versions; unparsable is never less.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	if len(pa) != 3 || len(pb) != 3 {
		return false
	}
	for i := range pa {
		x, e1 := strconv.Atoi(pa[i])
		y, e2 := strconv.Atoi(pb[i])
		if e1 != nil || e2 != nil {
			return false
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// Verdict judges one active card.
func Verdict(i Issue, in NightInput) string {
	if key, ok := strings.CutPrefix(i.Source, "sentinel:"); ok && i.Status != Fixed {
		for _, f := range in.Findings {
			if f.Kind+":"+f.Subject == key {
				return NightStill
			}
		}
		return NightGone
	}
	if i.Status == Fixed {
		if i.Release != "" && in.Prod != "" && !versionLess(in.Prod, i.Release) {
			return NightAwaitAccept
		}
		return NightAwaitRelease
	}
	if i.AgentTask != "" && in.Agent != nil {
		if st := in.Agent(i.AgentTask); st != "" && st != "running" && st != "queued" {
			return NightAgentDone
		}
	}
	if in.Now.Sub(i.UpdatedAt) > 7*24*time.Hour {
		return NightStale
	}
	return NightOpen
}

// NightReview judges every active card, writes the report next to the
// registry and appends a dated line to cards whose verdict changed.
func (s *Store) NightReview(in NightInput) (NightReport, error) {
	prev := ReadNight(s.nightPath())
	since := map[string]NightCard{}
	for _, c := range prev.Cards {
		since[c.ID] = c
	}
	list, _, err := s.List(Filter{Status: "active", Limit: maxIssues})
	if err != nil {
		return NightReport{Result: runoutcome.Resolve(0, true, "issue_registry_unavailable", "check registry access before retrying night review")}, err
	}
	day := in.Now.Format("02.01")
	rep := NightReport{At: in.Now.UTC(), Prod: in.Prod, Autonomy: in.Autonomy}
	blocker, next := "", ""
	written := 0
	for _, sum := range list {
		i, err := s.Get(sum.ID)
		if err != nil {
			blocker, next = "issue_card_unreadable", "inspect issue registry access and rerun failed cards"
			continue
		}
		c := NightCard{ID: i.ID, Verdict: Verdict(i, in), Since: day}
		if old, ok := since[i.ID]; ok && old.Verdict == c.Verdict {
			c.Since = old.Since
		} else if c.Verdict != NightOpen {
			if _, err := s.Update(i.ID, Patch{Append: "**Ночной разбор " + day + ":** " + nightLabel[c.Verdict] + nightHint(c.Verdict)}, "mcpshka-night"); err != nil {
				blocker, next = "issue_verdict_update_failed", "inspect issue registry permissions and retry the affected cards"
				continue
			}
			written++
		}
		rep.Cards = append(rep.Cards, c)
	}
	rep.Result = runoutcome.Resolve(len(rep.Cards), len(list) > 0, blocker, next)
	data, err := json.MarshalIndent(rep, "", "  ")
	if err == nil {
		tmp := s.nightPath() + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, s.nightPath())
		}
	}
	if err != nil {
		rep.Result = runoutcome.Resolve(written, len(list) > 0, "night_report_write_failed", "inspect report storage before retrying the night review")
	}
	return rep, err
}

func nightHint(v string) string {
	switch v {
	case NightGone:
		return " — сторож его больше не видит; держится сутки → issue_accept с host_triage."
	case NightAwaitAccept:
		return " — проверить приёмку карточки и issue_accept."
	case NightAgentDone:
		return " — agent_task_status, ревью ветки, тесты."
	}
	return "."
}

func (s *Store) nightPath() string { return filepath.Join(filepath.Dir(s.path), "night.json") }

// NightPath is the report file for a registry at path.
func NightPath(registry string) string { return filepath.Join(filepath.Dir(registry), "night.json") }

func ReadNight(path string) NightReport {
	var r NightReport
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	return r
}

// NightBrief is the morning line for MOTD and a new chat; empty when the
// report is missing or older than 36 h.
func NightBrief(r NightReport, now time.Time) string {
	if r.At.IsZero() || now.Sub(r.At) > 36*time.Hour {
		return ""
	}
	by := map[string][]string{}
	for _, c := range r.Cards {
		by[c.Verdict] = append(by[c.Verdict], c.ID)
	}
	parts := []string{}
	for _, v := range nightOrder {
		ids := by[v]
		if len(ids) == 0 || v == NightOpen {
			continue
		}
		sort.Strings(ids)
		shown := ids
		if len(shown) > 3 {
			shown = append(append([]string{}, shown[:3]...), "…")
		}
		parts = append(parts, fmt.Sprintf("%s %d (%s)", nightLabel[v], len(ids), strings.Join(shown, " ")))
	}
	line := fmt.Sprintf("Ночной разбор %s: карточек %d", r.At.Local().Format("02.01 15:04"), len(r.Cards))
	if len(parts) > 0 {
		line += " · " + strings.Join(parts, " · ")
	}
	if r.Autonomy != "" {
		line += "\n" + r.Autonomy
	}
	return line
}

const (
	nightStartHour = 22
	nightEndHour   = 6
)

// NightDue runs once per local night, starting in the evening, on a quiet
// host (load below 0.5 per CPU); the last hour runs even on a busy host.
func NightDue(now time.Time, last time.Time, loadPerCPU float64) bool {
	h := now.Hour()
	if (h < nightStartHour && h >= nightEndHour) || (loadPerCPU >= 0.5 && h != nightEndHour-1) {
		return false
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), nightStartHour, 0, 0, 0, now.Location())
	if h < nightEndHour {
		start = start.AddDate(0, 0, -1)
	}
	return last.IsZero() || last.Before(start)
}
