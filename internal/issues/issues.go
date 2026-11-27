// Package issues is MCPshka's problem registry: one durable list of cards
// (MCP-NNN) with priority, status, the commit and release that fixed them and
// an acceptance record. It replaces docs/ISSUE_REGISTRY_*.md, which lived
// outside Git and outside the interface and was edited by hand.
//
// Lifecycle: open → in_progress → fixed (commit required) → closed only by
// Accept with verified evidence; rejected (duplicate, not reproduced) needs a
// note; reopen goes back to open. Every change is a history event.
package issues

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	Open       = "open"
	InProgress = "in_progress"
	Fixed      = "fixed"
	Closed     = "closed"
	Rejected   = "rejected"

	maxBody    = 64 << 10
	maxHistory = 64
	maxIssues  = 4096
)

var (
	ErrNotFound = errors.New("issue not found")
	idPattern   = regexp.MustCompile(`^(MCP|PRJ)-[0-9]{3,5}$`)
	commitRe    = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	releaseRe   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
)

type Event struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Note   string    `json:"note,omitempty"`
}

type Acceptance struct {
	Evidence string    `json:"evidence"`
	Note     string    `json:"note,omitempty"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
}

type DiscoveryDefect struct {
	ChosenTools        []string `json:"chosen_tools,omitempty"`
	NativeAlternatives []string `json:"reported_native_alternatives,omitempty"`
	RegressionCase     string   `json:"regression_case"`
}

type Issue struct {
	DiscoveryComplaints uint64            `json:"catalog_discovery_complaints,omitempty"`
	DiscoveryCases      []DiscoveryDefect `json:"catalog_discovery_cases,omitempty"`
	HistoricalRisk      string            `json:"historical_risk,omitempty"`
	ID                  string            `json:"id"`
	Title               string            `json:"title"`
	Priority            string            `json:"priority"`
	Status              string            `json:"status"`
	Category            string            `json:"category,omitempty"`
	Project             string            `json:"project,omitempty"`
	Body                string            `json:"body"`
	StatusNote          string            `json:"status_note,omitempty"`
	Source              string            `json:"source,omitempty"`
	Commit              string            `json:"commit,omitempty"`
	Release             string            `json:"release,omitempty"`
	AgentTask           string            `json:"agent_task,omitempty"`
	Dispatch            string            `json:"dispatch,omitempty"` // agent requested from the page, awaiting a Face ID window
	Lesson              string            `json:"lesson,omitempty"`   // knowledge record written on acceptance
	Acceptance          *Acceptance       `json:"acceptance,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	History             []Event           `json:"history,omitempty"`
}

// Summary is the list view: no body, no history.
type Summary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Priority  string    `json:"priority"`
	Status    string    `json:"status"`
	Category  string    `json:"category,omitempty"`
	Project   string    `json:"project,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Release   string    `json:"release,omitempty"`
	AgentTask string    `json:"agent_task,omitempty"`
	Dispatch  string    `json:"dispatch,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (i Issue) Summary() Summary {
	return Summary{i.ID, i.Title, i.Priority, i.Status, i.Category, i.Project, i.Commit, i.Release, i.AgentTask, i.Dispatch, i.UpdatedAt}
}

type Store struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

func NewStore(path string) *Store { return &Store{path: path, now: time.Now} }

type file struct {
	Version int      `json:"version"`
	Issues  []*Issue `json:"issues"`
}

func (s *Store) load() (map[string]*Issue, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*Issue{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("issue registry unreadable: %w", err)
	}
	out := make(map[string]*Issue, len(f.Issues))
	for _, i := range f.Issues {
		out[i.ID] = i
	}
	return out, nil
}

func (s *Store) save(all map[string]*Issue) error {
	list := make([]*Issue, 0, len(all))
	for _, i := range all {
		list = append(list, i)
	}
	sort.Slice(list, func(a, b int) bool { return idLess(list[a].ID, list[b].ID) })
	raw, err := json.MarshalIndent(file{Version: 1, Issues: list}, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func idLess(a, b string) bool {
	pa, na := splitID(a)
	pb, nb := splitID(b)
	if pa != pb {
		return pa < pb
	}
	return na < nb
}

func splitID(id string) (string, int) {
	p, n, _ := strings.Cut(id, "-")
	v, _ := strconv.Atoi(n)
	return p, v
}

func (s *Store) change(fn func(map[string]*Issue) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(all); err != nil {
		return err
	}
	return s.save(all)
}

func (s *Store) Get(id string) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Issue{}, err
	}
	i, ok := all[strings.ToUpper(strings.TrimSpace(id))]
	if !ok {
		return Issue{}, ErrNotFound
	}
	return *i, nil
}

type Filter struct {
	Status   string `json:"status,omitempty"`   // one status, "active" (open+in_progress+fixed) or empty for all
	Priority string `json:"priority,omitempty"` // P0..P3
	Project  string `json:"project,omitempty"`
	Text     string `json:"text,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type Counts map[string]int

// List returns matching summaries, most urgent first, plus counts by status.
func (s *Store) List(f Filter) ([]Summary, Counts, error) {
	s.mu.Lock()
	all, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	counts := Counts{}
	words := tokens(f.Text)
	var out []Summary
	for _, i := range all {
		counts[i.Status]++
		counts["P:"+i.Priority]++
		switch {
		case f.Status == "active" && i.Status != Open && i.Status != InProgress && i.Status != Fixed:
			continue
		case f.Status != "" && f.Status != "active" && i.Status != f.Status:
			continue
		case f.Priority != "" && i.Priority != strings.ToUpper(f.Priority):
			continue
		case f.Project != "" && !strings.EqualFold(i.Project, f.Project):
			continue
		case len(words) > 0 && overlap(words, tokens(i.ID+" "+i.Title+" "+i.Category+" "+i.Body)) < len(words):
			continue
		}
		out = append(out, i.Summary())
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Priority != out[b].Priority {
			return out[a].Priority < out[b].Priority
		}
		return idLess(out[a].ID, out[b].ID)
	})
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, counts, nil
}

type NewIssue struct {
	Title    string `json:"title"`
	Priority string `json:"priority"`
	Category string `json:"category,omitempty"`
	Project  string `json:"project,omitempty"`
	Body     string `json:"body"`
	Source   string `json:"source,omitempty"`
}

func normPriority(p string) (string, error) {
	p = strings.ToUpper(strings.TrimSpace(p))
	if p == "P0" || p == "P1" || p == "P2" || p == "P3" {
		return p, nil
	}
	return "", errors.New("priority must be P0, P1, P2 or P3")
}

func cleanText(field, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New(field + " is required")
	}
	if len(v) > max {
		return "", fmt.Errorf("%s exceeds %d bytes", field, max)
	}
	return v, nil
}

// Create assigns the next MCP-NNN id. Duplicates are the caller's concern:
// Similar is offered before creating.
func (s *Store) Create(in NewIssue, actor string) (Issue, error) {
	title, err := cleanText("title", in.Title, 300)
	if err != nil {
		return Issue{}, err
	}
	body, err := cleanText("body", in.Body, maxBody)
	if err != nil {
		return Issue{}, err
	}
	prio, err := normPriority(in.Priority)
	if err != nil {
		return Issue{}, err
	}
	var out Issue
	err = s.change(func(all map[string]*Issue) error {
		if len(all) >= maxIssues {
			return errors.New("issue registry is full")
		}
		next := 0
		for id := range all {
			if p, n := splitID(id); p == "MCP" && n > next {
				next = n
			}
		}
		now := s.now().UTC()
		i := &Issue{ID: fmt.Sprintf("MCP-%03d", next+1), Title: title, Priority: prio, HistoricalRisk: prio, Status: Open, Category: strings.TrimSpace(in.Category), Project: strings.TrimSpace(in.Project), Body: body, Source: strings.TrimSpace(in.Source), CreatedAt: now, UpdatedAt: now}
		i.event(now, actor, "create", "")
		all[i.ID] = i
		out = *i
		return nil
	})
	return out, err
}

func (i *Issue) event(at time.Time, actor, action, note string) {
	if len(note) > 500 {
		note = note[:500]
	}
	i.History = append(i.History, Event{At: at, Actor: actor, Action: action, Note: note})
	if len(i.History) > maxHistory {
		i.History = i.History[len(i.History)-maxHistory:]
	}
	i.UpdatedAt = at
}

// Patch changes a card. Status cannot become closed here: only Accept closes.
type Patch struct {
	Discovery *DiscoveryDefect `json:"discovery_defect,omitempty"`
	Status    string           `json:"status,omitempty"`
	Priority  string           `json:"priority,omitempty"`
	Commit    string           `json:"commit,omitempty"`
	Release   string           `json:"release,omitempty"`
	AgentTask string           `json:"agent_task,omitempty"`
	Dispatch  *string          `json:"-"`
	Note      string           `json:"note,omitempty"`
	Append    string           `json:"append,omitempty"` // text added to the body (findings, repro)
}

func (s *Store) Update(id string, p Patch, actor string) (Issue, error) {
	if p.Status == Closed {
		return Issue{}, transition(nil, Closed, "")
	}
	var out Issue
	err := s.change(func(all map[string]*Issue) error {
		i, ok := all[strings.ToUpper(strings.TrimSpace(id))]
		if !ok {
			return ErrNotFound
		}
		now := s.now().UTC()
		if p.Discovery != nil {
			d := *p.Discovery
			if len(d.ChosenTools) > 8 || len(d.NativeAlternatives) > 8 || strings.TrimSpace(d.RegressionCase) == "" || len(d.RegressionCase) > 1024 {
				return errors.New("Укажите сценарий регрессии до 1024 байт и до восьми инструментов в каждом списке")
			}
			for _, name := range append(append([]string{}, d.ChosenTools...), d.NativeAlternatives...) {
				if len(name) == 0 || len(name) > 128 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
					return errors.New("Недопустимое имя инструмента в жалобе")
				}
			}
			if i.DiscoveryComplaints < ^uint64(0) {
				i.DiscoveryComplaints++
			}
			i.DiscoveryCases = append(i.DiscoveryCases, d)
			if len(i.DiscoveryCases) > 16 {
				i.DiscoveryCases = i.DiscoveryCases[len(i.DiscoveryCases)-16:]
			}
			if i.Category == "" {
				i.Category = "catalog-discovery"
			}
			i.event(now, actor, "catalog_discovery_complaint", "")
		}
		i.HistoricalRisk = historicalRisk(*i)
		if p.Priority != "" {
			prio, err := normPriority(p.Priority)
			if err != nil {
				return err
			}
			if prio < i.HistoricalRisk {
				i.HistoricalRisk = prio
			}
			if prio != i.Priority {
				i.event(now, actor, "priority "+i.Priority+"→"+prio, p.Note)
				i.Priority = prio
			}
		}
		if p.Commit != "" {
			if !commitRe.MatchString(p.Commit) {
				return errors.New("commit must be a hex commit id")
			}
			i.Commit = p.Commit
			i.event(now, actor, "commit "+shortCommit(p.Commit), p.Note)
		}
		if p.Release != "" {
			if !releaseRe.MatchString(p.Release) {
				return errors.New("release must be a version like 0.7.0")
			}
			i.Release = p.Release
			i.event(now, actor, "release "+p.Release, p.Note)
		}
		if p.AgentTask != "" {
			i.AgentTask = p.AgentTask
			i.event(now, actor, "agent_task "+p.AgentTask, p.Note)
		}
		if p.Dispatch != nil {
			i.Dispatch = *p.Dispatch
		}
		if p.Append != "" {
			if len(i.Body)+len(p.Append) > maxBody {
				return fmt.Errorf("body would exceed %d bytes", maxBody)
			}
			i.Body = strings.TrimRight(i.Body, "\n") + "\n\n**" + now.Format("02.01.2006") + " · " + actor + ".** " + strings.TrimSpace(p.Append)
			i.event(now, actor, "append", "")
		}
		if p.Status != "" && p.Status != i.Status {
			if err := transition(i, p.Status, p.Note); err != nil {
				return err
			}
			if p.Status == Open {
				i.Acceptance = nil
			}
			i.event(now, actor, i.Status+"→"+p.Status, p.Note)
			i.Status = p.Status
		} else if p.Note != "" && p.Status == "" && p.Priority == "" && p.Commit == "" && p.Release == "" && p.AgentTask == "" {
			i.event(now, actor, "note", p.Note)
		}
		out = *i
		return nil
	})
	return out, err
}

func transition(i *Issue, to, note string) error {
	switch to {
	case Open, InProgress:
		return nil
	case Fixed:
		if i.Commit == "" {
			return errors.New("fixed needs the commit that fixes it (commit=…)")
		}
		return nil
	case Rejected:
		if strings.TrimSpace(note) == "" {
			return errors.New("rejected needs a note: duplicate of …, not reproduced on …")
		}
		return nil
	case Closed:
		return errors.New("closed only by acceptance (issue_accept) with verified evidence")
	default:
		return errors.New("status must be open, in_progress, fixed or rejected")
	}
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// Accept closes a fixed card. verify proves the evidence (a finished
// successful job, an installed release); a claim without proof never closes.
func (s *Store) Accept(id, evidence, note, actor string, verify func(Issue, string) error) (Issue, error) {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" || len(evidence) > 200 {
		return Issue{}, errors.New("evidence is required: job:<id> of a passing test or release:<version> installed")
	}
	var out Issue
	err := s.change(func(all map[string]*Issue) error {
		i, ok := all[strings.ToUpper(strings.TrimSpace(id))]
		if !ok {
			return ErrNotFound
		}
		if i.Status != Fixed {
			return errors.New("only a fixed card is accepted; mark it fixed with its commit first")
		}
		if verify == nil {
			return errors.New("acceptance evidence cannot be verified")
		}
		if err := verify(*i, evidence); err != nil {
			return fmt.Errorf("acceptance refused: %w", err)
		}
		now := s.now().UTC()
		i.Acceptance = &Acceptance{Evidence: evidence, Note: note, By: actor, At: now}
		i.event(now, actor, "fixed→closed", evidence+" "+note)
		i.Status = Closed
		i.Dispatch = ""
		out = *i
		return nil
	})
	return out, err
}

// SetLesson links the knowledge record written for an accepted card.
func (s *Store) SetLesson(id, lesson string) error {
	return s.change(func(all map[string]*Issue) error {
		i, ok := all[id]
		if !ok {
			return ErrNotFound
		}
		i.Lesson = lesson
		return nil
	})
}

// Similar ranks existing cards by shared words, to show duplicates (and the
// fixes of closed look-alikes) before a new card is opened.
func (s *Store) Similar(text string, limit int) ([]Summary, error) {
	s.mu.Lock()
	all, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	words := tokens(text)
	if len(words) == 0 {
		return nil, nil
	}
	type scored struct {
		s Summary
		n int
	}
	var hits []scored
	for _, i := range all {
		if n := overlap(words, tokens(i.Title+" "+i.Category)); n*2 >= min(len(words), 6) && n >= 2 {
			hits = append(hits, scored{i.Summary(), n})
		}
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].n != hits[b].n {
			return hits[a].n > hits[b].n
		}
		return idLess(hits[a].s.ID, hits[b].s.ID)
	})
	var out []Summary
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.s)
	}
	return out, nil
}

var stop = map[string]bool{"и": true, "в": true, "на": true, "не": true, "с": true, "по": true, "для": true, "без": true, "the": true, "and": true, "of": true, "to": true, "a": true, "нет": true, "при": true, "из": true, "или": true, "что": true, "как": true}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if len([]rune(w)) < 3 && !strings.ContainsAny(w, "0123456789") || stop[w] {
			continue
		}
		// crude stem: Russian endings vary, the first 6 runes carry the word
		if r := []rune(w); len(r) > 6 {
			w = string(r[:6])
		}
		out[w] = true
	}
	return out
}

func overlap(a, b map[string]bool) int {
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return n
}
