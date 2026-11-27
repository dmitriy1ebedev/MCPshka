// Package roadmapitem turns «делай пункты roadmap» into a contract: one
// checkbox is one item, it carries its own exit criterion, and it is done
// only when it is closed. Lane G0 (11.11.2026): 60 commits named after
// GO0…GO5, not one checkbox closed — nothing told the chat what «done» is.
package roadmapitem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/failure"
	"mcpshka/internal/roadmapscan"
)

var (
	exitRe = regexp.MustCompile(`(?:Exit|Приёмка|Готово|DONE|Done)\**\s*[:—]\s*\**\s*(.+)$`)
)

// Entry is one checkbox of a phase.
type Entry struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Line     int    `json:"line"`
	State    string `json:"state"` // open, partial ([-]) or done
	Decision string `json:"decision,omitempty"`
	Exit     string `json:"exit,omitempty"`
}

// Phase is a roadmap section with its checkboxes and its prose (rules,
// mandatory tests), which applies to every item of the phase.
type Phase struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Line    int     `json:"line"`
	Entries []Entry `json:"entries"`
	Rules   string  `json:"rules,omitempty"`
}

// Order is the work order for the next item: what to do, when it is done,
// which stages lead there and how to report.
type Order struct {
	Root       string   `json:"root,omitempty"`
	Roadmap    string   `json:"roadmap,omitempty"`
	Phase      string   `json:"phase"`
	PhaseTitle string   `json:"phase_title"`
	Progress   string   `json:"progress"`
	Item       *Entry   `json:"item,omitempty"`
	AlsoOpen   []string `json:"also_open,omitempty"`
	PhaseRules string   `json:"phase_rules,omitempty"`
	Stages     []string `json:"stages"`
	DoneWhen   string   `json:"done_when"`
	Report     string   `json:"report"`
	Warning    string   `json:"warning,omitempty"`
}

const maxRules, maxText = 6000, 1200

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// Parse reads the phases the way project_context counts them (roadmapscan
// headings) plus the stream sections (S1–S5, C1…) whose items are closed
// the same way. A roadmap without phase headings is one queue «Q».
func Parse(content string) []Phase {
	lines, _ := roadmapscan.ContentLines(content)
	heads := roadmapscan.Headings(content)
	if len(heads) == 0 {
		heads = []roadmapscan.Heading{{ID: "Q", Title: "Очередь", Line: -1}}
	}
	phases := make([]Phase, 0, len(heads))
	for _, h := range heads {
		p := Phase{ID: h.ID, Title: h.Title, Line: h.Line + 1}
		var rules []string
		seen := map[string]bool{}
		exit := ""
		for i := h.Line + 1; i < len(lines); i++ {
			line := strings.TrimSuffix(lines[i], "\r")
			if level := roadmapscan.HeadingLevel(line); level > 0 && h.Level > 0 && level <= h.Level {
				break
			}
			m := roadmapscan.Checkbox.FindStringSubmatch(line)
			if m == nil {
				if t := strings.TrimSpace(line); t != "" && h.Level > 0 {
					rules = append(rules, t)
					if e := exitRe.FindStringSubmatch(t); e != nil && exit == "" {
						exit = e[1]
					}
				}
				continue
			}
			text := strings.TrimSpace(strings.TrimLeft(m[4], "* "))
			e := Entry{Text: clip(text, maxText), Line: i + 1, State: "open"}
			switch m[2] {
			case "-":
				e.State = "partial"
			case "x", "X":
				e.State = "done"
			case "~":
				if e.Decision = roadmapscan.Superseded(m[2], text); e.Decision != "" {
					e.State = "superseded"
				}
			}
			if id := roadmapscan.EntryID(text); id != "" && !seen[id] {
				e.ID = id
			} else {
				// Positional id; an explicit one may already hold the number
				// ("S5.6 Каталог" is S5's 4th box, the 6th one gets S5.6a).
				e.ID = fmt.Sprintf("%s.%d", h.ID, len(p.Entries)+1)
				for s := 'a'; seen[e.ID] && s <= 'z'; s++ {
					e.ID = fmt.Sprintf("%s.%d%c", h.ID, len(p.Entries)+1, s)
				}
			}
			seen[e.ID] = true
			if x := exitRe.FindStringSubmatch(text); x != nil {
				e.Exit = clip(strings.TrimSpace(x[1]), maxText)
			}
			p.Entries = append(p.Entries, e)
		}
		for i := range p.Entries {
			if p.Entries[i].Exit == "" {
				p.Entries[i].Exit = clip(exit, maxText)
			}
		}
		p.Rules = clip(strings.Join(rules, "\n"), maxRules)
		phases = append(phases, p)
	}
	return phases
}

func (p Phase) done() int {
	n := 0
	for _, e := range p.Entries {
		if e.State == "done" {
			n++
		}
	}
	return n
}

// Stages is the one ladder every item climbs. Several items share stages
// 3–4: one CI run and one release for the batch.
var Stages = []string{
	"1 code — реализуй пункт целиком до его Exit, не заготовку; коммиты «<ID>: …» (fs_write/fs_patch → project_commit)",
	"2 tests — тесты, которых требуют правила фазы; точечные project_test packages=[…]",
	"3 ci — forge_ci mode=publish, затем mode=wait на точном HEAD; phase_gate → CODE_TEST_PASS",
	"4 install — Exit говорит о работающей команде, службе или проде: выпусти и установи: любой проект — project_release по рецепту .mcpshka/release.json (нет рецепта — напиши его, mcpshka_guide topic=release; новый рецепт владелец подтверждает Face ID, ссылку дай и продолжай другие пункты), MCPshka — release_apply. Код в репозитории не равен установленному",
	"5 accept — проверь каждое условие Exit заданием MCPshka (exec_start/project_run → job id), запиши phase_checkpoint_record phase=<ID> criteria с evidence job:<id>",
	"6 close — roadmap_close items=[<ID>]: phase_gate phase=<ID> обязан дать PASS; отметит [x] и закоммитит ROADMAP",
}

const (
	doneWhen = "Пункт сделан, когда roadmap_close отметил его [x]. Коммиты, тесты и «заготовки» пункт не закрывают. Этапы install и accept — часть пункта, а не повод остановиться. Несколько пунктов: этапы 1–2 для всех, один CI и один выпуск на пачку, затем 5–6 по каждому."
	report   = "Владельцу отчитывайся закрытыми пунктами: «было N/M → стало K/M», закрытые ID; про незакрытый — на каком этапе стоит и что именно мешает. Число коммитов — не результат."
)

// Next builds the work order for the first unfinished item of the phase
// project_context names as current.
func Next(content string) (Order, error) {
	return NextPhase(content, "")
}

func NextPhase(content, want string) (Order, error) {
	phases := Parse(content)
	explicit := want != ""
	if want == "" {
		if snap, err := roadmapscan.Parse(content, ""); err == nil && snap.Next != nil {
			want = snap.Next.ID
		}
	}
	for _, p := range phases {
		if want != "" && p.ID != want {
			continue
		}
		o := Order{Phase: p.ID, PhaseTitle: p.Title, Progress: fmt.Sprintf("%d/%d", p.done(), len(p.Entries)), PhaseRules: p.Rules, Stages: Stages, DoneWhen: doneWhen, Report: report}
		for i := range p.Entries {
			if e := p.Entries[i]; e.State != "done" && e.State != "superseded" {
				if o.Item == nil {
					o.Item = &e
				} else {
					o.AlsoOpen = append(o.AlsoOpen, e.ID)
				}
			}
		}
		switch {
		case o.Item == nil && want == "":
			continue
		case o.Item == nil:
			if len(p.Entries) == 0 {
				o.Warning = "В фазе " + p.ID + " нет чекбоксов: запиши работу пунктами с проверяемым Exit."
			} else {
				o.Warning = "В фазе " + p.ID + " нет открытых чекбоксов."
			}
		case o.Item.Exit == "":
			o.Warning = "У пункта " + o.Item.ID + " нет Exit: до работы допиши в его строку «Exit: проверяемое условие», иначе его нечем принять."
		}
		return o, nil
	}
	if explicit {
		return Order{}, fmt.Errorf("roadmap has no section %s", want)
	}
	return Order{}, errors.New("roadmap has no open items")
}

// Close marks the named open items [x] with the date and a note. Items
// already done are returned in already and left untouched.
func Close(content string, ids []string, date, note string) (out string, closed, already []string, err error) {
	lines := strings.Split(content, "\n")
	byID := map[string]Entry{}
	for _, p := range Parse(content) {
		for _, e := range p.Entries {
			if old, ok := byID[e.ID]; !ok || old.State == "done" {
				byID[e.ID] = e
			}
		}
	}
	for _, id := range ids {
		e, ok := byID[id]
		switch {
		case !ok:
			return "", nil, nil, fmt.Errorf("roadmap has no item %s (ids come from roadmap_next)", id)
		case e.State == "superseded":
			return "", nil, nil, fmt.Errorf("roadmap item %s was superseded by %s", id, e.Decision)
		case e.State == "done":
			already = append(already, id)
			continue
		}
		raw := lines[e.Line-1]
		line := strings.TrimSuffix(raw, "\r")
		m := roadmapscan.Checkbox.FindStringSubmatch(line)
		if m == nil {
			return "", nil, nil, fmt.Errorf("item %s moved: read roadmap_next again", id)
		}
		mark := " — закрыт " + date
		if n := strings.TrimSpace(note); n != "" {
			mark += ": " + n
		}
		lines[e.Line-1] = m[1] + "[x]" + m[3] + m[4] + mark + raw[len(line):]
		closed = append(closed, id)
	}
	return strings.Join(lines, "\n"), closed, already, nil
}

// CloseFile applies Close to path with compare-and-swap, keeping its mode.
func CloseFile(path string, ids []string, date, note string) (closed, already []string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	out, closed, already, err := Close(string(raw), ids, date, note)
	if err != nil || len(closed) == 0 {
		return nil, already, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".roadmap-close-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(out); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), st.Mode().Perm())
	}
	if err != nil {
		return nil, nil, err
	}
	if again, rerr := os.ReadFile(path); rerr != nil || string(again) != string(raw) {
		return nil, nil, errors.New("ROADMAP changed concurrently; nothing closed, call roadmap_close again")
	}
	return closed, already, os.Rename(tmp.Name(), path)
}

// NextFile is Next for a roadmap file plus the honesty check: commits
// piling up while the roadmap stays untouched mean nothing was closed.
func NextFile(root, path string, commitsSinceRoadmap int, phase string) (Order, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Order{}, err
	}
	o, err := NextPhase(string(raw), phase)
	o.Root, o.Roadmap = root, path
	if err == nil && commitsSinceRoadmap >= 10 {
		o.Warning = strings.TrimSpace(o.Warning + fmt.Sprintf(" %d коммитов после последнего изменения ROADMAP: ни один пункт ими не закрыт. Доведи текущий до roadmap_close, новые заготовки не добавляй.", commitsSinceRoadmap))
	}
	return o, err
}

type CloseInput struct {
	Path           string   `json:"path" jsonschema:"Repository or worktree whose ROADMAP.md holds the items."`
	Items          []string `json:"items" jsonschema:"1..20 item ids from roadmap_next (item.id, also_open); closed together in one commit."`
	LeaseToken     string   `json:"lease_token" jsonschema:"Your project_lease_acquire token of this repository."`
	Note           string   `json:"note,omitempty" jsonschema:"One-line proof written into the roadmap line (≤200 chars): the release and the evidence."`
	IdempotencyKey string   `json:"idempotency_key" jsonschema:"Required retry key."`
}

type CloseResult struct {
	Closed  []string `json:"closed"`
	Already []string `json:"already_done,omitempty"`
	Commit  string   `json:"commit,omitempty"`
	Next    *Order   `json:"next,omitempty"`
}

// CloseAccepted marks items [x] only when blocker(id) is empty for every
// one (the item's own phase gate passes); a refusal changes nothing.
// commit records the edited roadmap and may return no revision.
func CloseAccepted(in CloseInput, file string, held error, blocker func(id string) string, commit func(msg string) (string, error)) (CloseResult, error) {
	refuse := func(code, msg string) (CloseResult, error) {
		return CloseResult{}, failure.Rejected(code, "roadmap_close", msg, nil)
	}
	if len(in.Items) == 0 || len(in.Items) > 20 || len(in.Note) > 200 || strings.ContainsAny(in.Note, "\r\n") {
		return refuse("ROADMAP_CLOSE_INPUT", "items: 1..20 ids; note: one line ≤200 chars")
	}
	if held != nil {
		return refuse("LEASE_NOT_HELD", held.Error())
	}
	for _, id := range in.Items {
		if why := blocker(id); why != "" {
			return refuse("ROADMAP_ITEM_NOT_ACCEPTED", "пункт "+id+" не принят, ROADMAP не изменён: "+why)
		}
	}
	var res CloseResult
	var err error
	if res.Closed, res.Already, err = CloseFile(file, in.Items, time.Now().Format("02.01.2006"), in.Note); err != nil {
		return refuse("ROADMAP_CLOSE_REFUSED", err.Error())
	}
	if len(res.Closed) > 0 {
		if res.Commit, err = commit("roadmap: close " + strings.Join(res.Closed, ", ")); err != nil {
			return res, err
		}
	}
	if raw, err := os.ReadFile(file); err == nil {
		if o, err := Next(string(raw)); err == nil {
			res.Next = &o
		}
	}
	return res, nil
}
