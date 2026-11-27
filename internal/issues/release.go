package issues

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// ShipRelease closes the cards the running release fixes: fixed cards of an
// already running release plus "Fixes MCP-NNN" commits that are ancestors of
// this version's release commit ("<version>: …" on main), so a fix merged but
// not yet released stays open. lesson writes the knowledge record of each
// accepted card (may be nil).
func (s *Store) ShipRelease(ctx context.Context, repo, version string, verify func(Issue, string) error, lesson func(Issue) string) (ShipResult, error) {
	if !releaseVersion.MatchString(version) {
		return ShipResult{}, fmt.Errorf("version %q is not a release", version)
	}
	git := func(args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(c, "git", append([]string{"-C", repo}, args...)...).Output()
		return string(out), err
	}
	head, err := git("log", "--format=%H", "-n", "1", "--grep=^"+regexp.QuoteMeta(version)+":", "main")
	head = strings.TrimSpace(head)
	if err != nil || head == "" {
		return ShipResult{}, fmt.Errorf("no %s: release commit on main", version)
	}
	raw, err := git("log", "--format=%H%x00%B%x1e", "-n", "400", head)
	if err != nil {
		return ShipResult{}, err
	}
	fixed := s.Shipped(version)
	// A fixed card without a release is shipped too when its commit is an
	// ancestor of the running release commit.
	if list, _, err := s.List(Filter{Status: Fixed, Limit: maxIssues}); err == nil {
		for _, i := range list {
			if i.Release == "" && i.Commit != "" {
				if _, err := git("merge-base", "--is-ancestor", i.Commit, head); err == nil {
					fixed[i.ID] = i.Commit
				}
			}
		}
	}
	for id, sha := range FixedBy(ParseLog(raw)) {
		fixed[id] = sha
	}
	r := s.Ship(fixed, version, verify)
	for _, id := range r.Accepted {
		if i, err := s.Get(id); err == nil && lesson != nil {
			if l := lesson(i); l != "" {
				_ = s.SetLesson(id, l)
			}
		}
	}
	return r, nil
}

// Finding is one host problem the sentinel saw (host_triage).
type Finding struct {
	Severity, Kind, Subject, Evidence, Action, Note string
	Priority                                        string // empty: P1
}

// FileFindings: a critical finding seen on two passes in a row (no flapping
// restarts) becomes one card per kind+subject while a card for it is still
// active. It returns this pass's critical keys and the filed card ids.
func (s *Store) FileFindings(findings []Finding, prev map[string]bool) (map[string]bool, []string) {
	now := map[string]bool{}
	var filed []string
	for _, f := range findings {
		if f.Severity != "critical" {
			continue
		}
		key := "sentinel:" + f.Kind + ":" + f.Subject
		now[key] = true
		if !prev[key] || s.activeFrom(key) {
			continue
		}
		body := "Сторож MCPshka (host_triage дважды подряд с интервалом 15 мин).\n\n**Факт:** " + f.Evidence
		if f.Action != "" {
			body += "\n**Предложенное действие:** " + f.Action
		}
		if f.Note != "" {
			body += "\n**Заметка:** " + f.Note
		}
		body += "\n\n**Приёмка:** host_triage без этой проблемы; исправляющий коммит с `Fixes <id>` закроет карточку выпуском."
		pr := f.Priority
		if pr == "" {
			pr = "P1"
		}
		i, err := s.Create(NewIssue{Title: shortTitle(f.Kind + " " + f.Subject + ": " + f.Evidence), Priority: pr, Category: "Сторож", Body: body, Source: key}, "mcpshka")
		if err == nil {
			filed = append(filed, i.ID)
		}
	}
	return now, filed
}

func (s *Store) activeFrom(source string) bool {
	list, _, err := s.List(Filter{Status: "active", Text: "Сторож", Limit: maxIssues})
	if err != nil {
		return true // unsure: do not file
	}
	for _, sum := range list {
		if i, err := s.Get(sum.ID); err == nil && i.Source == source {
			return true
		}
	}
	return false
}

func shortTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 150 {
		return string(r[:150]) + "…"
	}
	return s
}

// Watch runs the sentinel forever: every period, probe and file what persists.
func (s *Store) Watch(period time.Duration, probe func() []Finding, filed func([]string)) {
	seen := map[string]bool{}
	for {
		time.Sleep(period)
		var ids []string
		if seen, ids = s.FileFindings(probe(), seen); len(ids) > 0 && filed != nil {
			filed(ids)
		}
	}
}
