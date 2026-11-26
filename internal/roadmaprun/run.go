// Package roadmaprun drives a project through its ROADMAP without a chat: it
// takes the next open item, hands it to a coding agent (Codex CLI) with the
// MCPshka operating rules, then asks MCPshka's own phase_gate for the verdict.
// It stops on the owner's terms: a real blocker, a repeated failure, agent
// quota without a usable reset, the time budget or the phase count.
// The journal is the morning report.
package roadmaprun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/agents"
	"mcpshka/internal/roadmapnext"
	"mcpshka/internal/systemdsvc"
)

// StateDir holds one journal per run, readable by the MCPshka service.
const StateDir = "/var/lib/mcpshka/roadmap-runs"

var stateDir = StateDir

type Phase struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Attempts   int       `json:"attempts"`
	Verdict    string    `json:"verdict,omitempty"` // PASS, BLOCKED, ERROR
	Head       string    `json:"head,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Note       string    `json:"note,omitempty"`
	QuotaWaits []string  `json:"quota_waits,omitempty"`
}

type Journal struct {
	LastTestKey     string    `json:"last_test_key,omitempty"`
	DetailTruncated bool      `json:"detail_truncated,omitempty"`
	Goal            string    `json:"goal,omitempty"`
	CurrentPhase    string    `json:"current_phase,omitempty"`
	Agent           string    `json:"agent,omitempty"`
	Step            string    `json:"step,omitempty"`
	LastJob         string    `json:"last_job,omitempty"`
	LastGate        string    `json:"last_gate,omitempty"`
	Blocker         string    `json:"blocker,omitempty"`
	NextAction      string    `json:"next_action,omitempty"`
	ID              string    `json:"id"`
	Path            string    `json:"path"`
	Roadmap         string    `json:"roadmap"`
	StartedAt       time.Time `json:"started_at"`
	Deadline        time.Time `json:"deadline"`
	MaxPhases       int       `json:"max_phases"`
	State           string    `json:"state"` // running, done, stopped, interrupted (read side)
	StopReason      string    `json:"stop_reason,omitempty"`
	Phases          []Phase   `json:"phases"`
	UpdatedAt       time.Time `json:"updated_at"`
	// SupersededBy (read side): a newer run on the same path exists, so this
	// stopped run's next_action is history, not open work.
	SupersededBy string `json:"superseded_by,omitempty"`
}

type Config struct {
	secret    string
	Goal      string
	Test      func(context.Context, string, string, string, func(string) error) (job, head, next string, err error)
	Diagnose  func(context.Context) (string, error)
	ID        string
	Path      string        // repository (may be a worktree)
	Roadmap   string        // ROADMAP.md, defaults to the main checkout's
	MaxPhases int           // stop after this many PASS phases
	Budget    time.Duration // whole-run time budget
	PhaseMax  time.Duration // one agent attempt
	// Agents are tried in order; one that hits its usage limit is skipped
	// until its reset, and the run waits only when all are exhausted.
	Agents   []string
	RunAgent func(ctx context.Context, agent, dir, prompt string) (string, error)
	// Admit is the provider-independent admission check before delegation
	// (MCP-100): known auth failure or capacity refusal skips the agent and
	// the runner falls back to the next one instead of wasting an attempt.
	Admit func(ctx context.Context, agent string) (ok bool, reason string)
	Sleep func(ctx context.Context, d time.Duration) error
	Gate  Gate
	// Lease is held by the runner for the whole run and lent to every agent:
	// an agent that dies (usage limit) can no longer strand the project.
	Lease Lease
	Now   func() time.Time
}

// Gate returns MCPshka's phase_gate verdict and next action.
type Gate func(ctx context.Context, path, phase string) (verdict, head, next string, err error)

// Lease is the project's single-writer lease as the runner uses it.
type Lease interface {
	Acquire(ctx context.Context, path, owner, key string) (token string, err error)
	Heartbeat(ctx context.Context, path, token, key string) error
	Release(ctx context.Context, path, token, key string) error
}

var blockedLine = regexp.MustCompile(`(?m)^BLOCKED:\s*(.+)$`)

// MarkInterrupted reports a run whose runner the host reboot killed: the
// journal still says running but was last written before boot.
func MarkInterrupted(j Journal, boot time.Time) Journal {
	if boot.IsZero() || j.State != "running" || !j.UpdatedAt.Before(boot) {
		return j
	}
	j.State = "interrupted"
	j.StopReason = "хост перезагружен во время прогона; запусти roadmap_run снова — он продолжит со следующего незакрытого пункта (незакоммиченную работу агент увидит в git status)"
	return j
}

func (c Config) save(j *Journal) error {
	clean := func(value string) string {
		if c.secret != "" {
			value = strings.ReplaceAll(value, c.secret, "[REDACTED]")
		}
		return systemdsvc.DiagnosticText(value)
	}
	j.StopReason, j.Blocker, j.NextAction, j.LastGate = clean(j.StopReason), clean(j.Blocker), clean(j.NextAction), clean(j.LastGate)
	for i := range j.Phases {
		j.Phases[i].Note = clean(j.Phases[i].Note)
	}
	j.UpdatedAt = c.Now().UTC()
	raw, err := json.MarshalIndent(j, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		_ = os.Chown(stateDir, 994, 994) // the MCPshka service reads journals
	}
	tmp := filepath.Join(stateDir, j.ID+".json.tmp")
	if err := os.WriteFile(tmp, raw, 0640); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		_ = os.Chown(tmp, 994, 994)
	}
	return os.Rename(tmp, filepath.Join(stateDir, j.ID+".json"))
}

// Prompt is everything a fresh agent needs for one phase; no prior context.
func Prompt(path string, it roadmapnext.Item, rules, blocker, token string) string {
	var b strings.Builder
	if it.ID == "" {
		fmt.Fprintf(&b, "Ты исполнитель MCPshka. Проект: %s. Задача владельца: %s.\n\n", path, it.Title)
	} else {
		fmt.Fprintf(&b, "Ты исполнитель MCPshka. Проект: %s. Задача — довести до PASS одну фазу roadmap: %s (%s).\n\n", path, it.ID, it.Title)
	}
	b.WriteString("Открытые пункты фазы:\n")
	for _, o := range it.Open {
		fmt.Fprintf(&b, "- %s\n", o)
	}
	if it.Acceptance != "" {
		fmt.Fprintf(&b, "Критерий готовности: %s\n", it.Acceptance)
	}
	if blocker != "" {
		fmt.Fprintf(&b, "\nПрошлая попытка остановилась так: %s\nУстрани это и продолжай.\n", blocker)
	}
	fmt.Fprintf(&b, "\nLease проекта уже держит раннер MCPshka: lease_token=%s — передавай его в инструменты записи и project_test. НЕ вызывай project_lease_acquire и project_lease_release.\n", token)
	b.WriteString("Перед sealed проверкой проверь project_context: если профиль не зарегистрирован, project_onboard_preview → проверь профиль → project_onboard под текущим lease; не заменяй существующий профиль вслепую.\n")
	b.WriteString("Порядок: минимальная реализация → точечные project_test → коммит чистого HEAD. Раннер сам запускает sealed project_test и ждёт job_wait; слова агента не заменяют тест. Не трогай чужие файлы. Не обходи защиту MCPshka.\n")
	if it.ID == "" {
		b.WriteString("Production-изменения выполняй только если это явно разрешено задачей владельца, через штатные инструменты и обязательные test/approval/fencing gates. Face ID не заменяет fencing; UNKNOWN сначала сверяй, не повторяй вслепую.\n")
	} else {
		b.WriteString("Не трогай прод.\n")
	}
	if it.ID != "" {
		fmt.Fprintf(&b, "После теста: phase_gate path=<проект> phase=%s → при PASS отметь пункты фазы [x] в локальном ROADMAP. Заверши все нужные коммиты до возврата раннеру.\nПоследняя строка: PHASE_DONE %s или BLOCKED: <точная причина>.\n", it.ID, it.ID)
	} else {
		b.WriteString("Не меняй ROADMAP ради произвольной задачи; phase_gate без фазы не вызывай. Последняя строка: TASK_DONE или BLOCKED: <точная причина>.\n")
	}
	b.WriteString("Правила MCPshka:\n" + rules)
	b.WriteString("\nУказание исполнителю: не запускай вложенный roadmap_run или других агентов. Выполняй именно порученную задачу. Журналы своих jobs читай bounded; не проси владельца запускать journalctl. Владелец нужен только для Face ID, отсутствующих полномочий или настоящего внешнего блокера.\n")
	b.WriteString("Обычные проекты: разрешённое задачей ff-merge под lease целевого worktree + sealed project_test чистого HEAD. integration_ship только для защищённого MCPshka. Не вливай без разрешения задачи.\n")
	return b.String()
}

// Run executes phases until a stop condition and returns the final journal.
func Run(ctx context.Context, c Config, rules string) (*Journal, error) {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.PhaseMax <= 0 {
		c.PhaseMax = 2 * time.Hour
	}
	if _, err := ParseAllowedAgents("agents:" + strings.Join(c.Agents, ",")); err != nil {
		return nil, err
	}
	if c.Sleep == nil {
		c.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	ctx, cancelRun := context.WithTimeout(ctx, c.Budget)
	defer cancelRun()
	ctx, revoke := context.WithCancelCause(ctx)
	defer revoke(nil)
	exhausted := map[string]time.Time{}
	var denied []string
	pick := func() (string, time.Time) {
		var earliest time.Time
		denied = denied[:0]
		for _, a := range c.Agents {
			reset, out := exhausted[a]
			if !out || (!reset.IsZero() && !c.Now().Before(reset)) {
				if c.Admit != nil {
					if ok, reason := c.Admit(ctx, a); !ok {
						denied = append(denied, a+": "+reason)
						continue
					}
				}
				return a, time.Time{}
			}
			if !reset.IsZero() && (earliest.IsZero() || reset.Before(earliest)) {
				earliest = reset
			}
		}
		return "", earliest
	}
	goal := c.Goal
	if goal == "" {
		goal = "ROADMAP: " + c.Roadmap
	}
	j := &Journal{Goal: goal, Step: "acquire_lease", NextAction: "observe roadmap_run_status", ID: c.ID, Path: c.Path, Roadmap: c.Roadmap, StartedAt: c.Now().UTC(), Deadline: c.Now().Add(c.Budget).UTC(), MaxPhases: c.MaxPhases, State: "running"}
	stop := func(reason string) (*Journal, error) {
		if cause := context.Cause(ctx); cause != nil {
			reason += "; " + cause.Error()
		}
		reason = readableBlocker(reason)
		j.State, j.StopReason = "stopped", reason
		j.Step, j.Blocker, j.NextAction = "blocked", reason, reason
		return j, c.save(j)
	}
	if err := c.save(j); err != nil {
		return j, err
	}
	token := ""
	retainLease := false
	if c.Lease != nil {
		var err error
		token, err = c.Lease.Acquire(ctx, c.Path, "roadmap-run:"+c.ID, "roadmap-run-"+c.ID+"-acquire")
		if err != nil {
			return stop("lease проекта занят или недоступен: " + err.Error())
		}
		c.secret = token
		beat, stopBeat := context.WithCancel(ctx)
		beatDone := make(chan struct{})
		defer func() {
			stopBeat()
			<-beatDone
			if retainLease || context.Cause(ctx) != nil {
				j.State, j.Step = "stopped", "reconciliation_required"
				j.NextAction = "reconcile project_context, project_lease_status and last_job " + j.LastJob + "; retain WIP and runner lease; recover original acquire roadmap-run-" + c.ID + "-acquire before any release or successor"
				_ = c.save(j)
				return
			}
			releaseCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := c.Lease.Release(releaseCtx, c.Path, token, "roadmap-run-"+c.ID+"-release"); err != nil {
				j.State, j.Step = "stopped", "cleanup_required"
				j.Blocker = "runner lease release unverified: " + err.Error()
				j.NextAction = "reconcile last_job " + j.LastJob + " and project_lease_status; resume original acquire identity before releasing this runner lease"
				_ = c.save(j)
			}
		}()
		go func() {
			defer close(beatDone)
			for n := 1; ; n++ {
				select {
				case <-beat.Done():
					return
				case <-time.After(time.Minute):
					if err := c.Lease.Heartbeat(beat, c.Path, token, fmt.Sprintf("roadmap-run-%s-beat-%d", c.ID, n)); err != nil {
						if beat.Err() != nil {
							return
						}
						revoke(fmt.Errorf("lease heartbeat unverified: %w", err))
						return
					}
				}
			}
		}()
	}
	passed := 0
	for {
		if passed >= c.MaxPhases {
			j.State, j.StopReason = "done", fmt.Sprintf("выполнено %d фаз — лимит прогона", passed)
			j.Step, j.Blocker, j.NextAction = "complete", "", "none"
			return j, c.save(j)
		}
		if !c.Now().Before(j.Deadline) {
			return stop("исчерпан бюджет времени прогона")
		}
		var it roadmapnext.Item
		if c.Goal != "" {
			if c.Test == nil {
				return stop("sealed project_test capability missing; configure runner Test before retry")
			}
			if passed > 0 {
				j.State, j.Step, j.NextAction = "done", "complete", "none"
				j.Blocker = ""
				return j, c.save(j)
			}
			it = roadmapnext.Item{Title: c.Goal}
		} else {
			raw, err := os.ReadFile(c.Roadmap)
			if err != nil {
				return stop("ROADMAP недоступен: " + err.Error())
			}
			var nextErr error
			it, nextErr = roadmapnext.Next(string(raw))
			if nextErr != nil {
				j.State, j.StopReason, j.Step, j.NextAction = "done", "в roadmap нет открытых пунктов", "complete", "none"
				return j, c.save(j)
			}
		}
		j.CurrentPhase = it.ID
		if n := len(j.Phases); n > 0 && j.Phases[n-1].ID == it.ID && j.Phases[n-1].Verdict == "PASS" {
			return stop(it.ID + " прошла гейт, но осталась открытой в ROADMAP — агент не отметил пункты")
		}
		j.Phases = append(j.Phases, Phase{ID: it.ID, Title: it.Title, StartedAt: c.Now().UTC()})
		ph := &j.Phases[len(j.Phases)-1]
		stopPhase := func(reason string) (*Journal, error) {
			ph.Verdict, ph.Note, ph.FinishedAt = "ERROR", reason, c.Now().UTC()
			return stop(reason)
		}
		blocker, nextAction := "", ""
		for ph.Attempts < 2 {
			if ctx.Err() != nil {
				return stopPhase("прогон прерван: " + ctx.Err().Error())
			}
			if !c.Now().Before(j.Deadline) {
				return stopPhase("исчерпан бюджет времени прогона")
			}
			agent, wait := pick()
			if agent == "" {
				if wait.IsZero() && len(denied) > 0 {
					stopped, err := stopPhase("ни один агент не допущен (admission HOLD): " + strings.Join(denied, "; "))
					stopped.NextAction = "restore the refused agent (login/capacity) shown in the blocker, then start a new run; no attempt was spent"
					return stopped, err
				}
				if wait.IsZero() {
					return stopPhase("лимиты всех агентов исчерпаны; время сброса неизвестно")
				}
				if !wait.Before(j.Deadline) {
					return stopPhase("лимиты всех агентов исчерпаны; сброс с запасом 1 мин позже или на границе бюджета прогона: " + wait.Format(time.RFC3339))
				}
				j.Step = "quota_wait"
				j.NextAction = "wait for the recorded quota reset; unknown reset requires provider evidence"
				ph.Note = "ждём до " + wait.Format(time.RFC3339) + " (сброс лимита агента + 1 мин)"
				ph.QuotaWaits = append(ph.QuotaWaits, ph.Note)
				if err := c.save(j); err != nil {
					return j, err
				}
				if err := c.Sleep(ctx, wait.Sub(c.Now())); err != nil {
					return stopPhase("прогон прерван во время ожидания сброса лимита: " + err.Error())
				}
				continue
			}
			left := j.Deadline.Sub(c.Now())
			if left > c.PhaseMax {
				left = c.PhaseMax
			}
			ph.Agent = agent
			if len(denied) > 0 {
				ph.Note = "admission fallback: " + strings.Join(denied, "; ")
			}
			j.Agent, j.Step, j.NextAction = agent, "agent", "observe roadmap_run_status"
			ph.Attempts++
			if err := c.save(j); err != nil {
				return j, err
			}
			actx, cancel := context.WithTimeout(ctx, left)
			tail, aerr := c.RunAgent(actx, agent, c.Path, Prompt(c.Path, it, rules, blocker, token))
			attemptErr := actx.Err()
			cancel()
			if ctx.Err() != nil {
				return stopPhase("прогон прерван: " + ctx.Err().Error())
			}
			if attemptErr != nil {
				retainLease = true
				return stopPhase("agent attempt outcome UNKNOWN after cancellation: " + attemptErr.Error())
			}
			if q, ok := agents.IsQuota(aerr); ok {
				reset := q.Reset
				if !reset.IsZero() {
					reset = reset.Add(time.Minute)
					// A stale reset must not cause a tight loop of quota retries.
					if !reset.After(c.Now()) {
						reset = time.Time{}
					}
				}
				exhausted[agent] = reset
				ph.Attempts-- // a limit is not an attempt at the phase
				continue
			}
			verdict, head, next := "PASS", "", ""
			var gerr error
			completion := strings.Split(strings.TrimSpace(tail), "\n")
			if c.Goal != "" && (aerr != nil || completion[len(completion)-1] != "TASK_DONE") {
				verdict, next = "BLOCKED", "retry with bounded own-unit diagnosis; repeated agent refusal requires correcting the recorded blocker or an available provider"
			}
			if c.Test != nil && verdict == "PASS" {
				j.Step = "sealed_project_test"
				j.LastTestKey = fmt.Sprintf("roadmap-run-%s-test-%d-%d", c.ID, len(j.Phases), ph.Attempts)
				if err := c.save(j); err != nil {
					return j, err
				}
				job, testHead, testNext, testErr := c.Test(ctx, c.Path, token, j.LastTestKey, func(id string) error { j.LastJob = id; return c.save(j) })
				j.LastJob, head = job, testHead
				if testErr != nil {
					retainLease = true
					return stopPhase("sealed project_test outcome UNKNOWN; reconcile project_context and job " + job + "; retain/replay the original project_test key " + j.LastTestKey + " after reconciliation: " + testErr.Error())
				}
				if testNext != "" {
					verdict, next = "BLOCKED", testNext
				}
			}
			if verdict == "PASS" && it.ID != "" {
				j.Step = "phase_gate"
				if err := c.save(j); err != nil {
					return j, err
				}
				if c.Gate == nil {
					return stopPhase("phase_gate capability missing")
				}
				testedHead := head
				verdict, head, next, gerr = c.Gate(ctx, c.Path, it.ID)
				if c.Test != nil && gerr == nil && verdict == "PASS" && head != testedHead {
					verdict, next = "BLOCKED", "phase_gate HEAD differs from sealed project_test HEAD; reconcile before acceptance"
				}
			}
			ph.Head = head
			switch {
			case gerr == nil && verdict == "PASS":
				ph.Verdict, ph.Note = "PASS", "sealed project_test PASS"
				if it.ID != "" {
					ph.Note = "phase_gate PASS"
				}
			case gerr != nil:
				ph.Verdict, ph.Note = "ERROR", "phase_gate: "+gerr.Error()
			default:
				ph.Verdict, ph.Note = "BLOCKED", next
			}
			nextAction = next
			j.LastGate = ph.Note
			if c.Goal != "" && ph.Verdict != "PASS" && blockedLine.FindStringSubmatch(tail) == nil {
				ph.Note += "; agent report: " + systemdsvc.DiagnosticText(tail)
			}
			if m := blockedLine.FindStringSubmatch(tail); m != nil && ph.Verdict != "PASS" {
				ph.Note = "агент: " + strings.TrimSpace(m[1]) + "; гейт: " + ph.Note
			}
			if aerr != nil && ph.Verdict != "PASS" {
				ph.Note += "; агент завершился: " + aerr.Error()
			}
			if ph.Verdict == "PASS" {
				// MCP-004: the runner, not the agent, closes the accepted
				// phase in ROADMAP (atomic compare-and-swap write).
				if c.Goal == "" && it.ID != "" {
					if n, merr := roadmapnext.MarkDoneFile(c.Roadmap, it); merr != nil {
						ph.Note += "; ROADMAP not closed: " + merr.Error()
						j.NextAction = "reconcile ROADMAP " + it.ID + ": mark its items [x] after reviewing the concurrent edit"
					} else if n > 0 {
						ph.Note += fmt.Sprintf("; ROADMAP closed %d item(s)", n)
					}
				}
				break
			}
			if c.Diagnose != nil {
				excerpt, err := c.Diagnose(ctx)
				if err != nil {
					ph.Note += "; own unit journal unavailable (UNVERIFIED)"
				} else if excerpt != "" {
					ph.Note += "; own unit: " + excerpt
				}
			}
			if token != "" {
				ph.Note = strings.ReplaceAll(ph.Note, token, "[REDACTED]")
			}
			ph.Note = systemdsvc.DiagnosticText(ph.Note)
			j.Blocker, j.NextAction = ph.Note, "runner retries with this cause; after repeated failure correct the recorded blocker"
			blocker = ph.Note
		}
		ph.FinishedAt = c.Now().UTC()
		if ph.Verdict != "PASS" {
			j, err := stop(ph.ID + " не прошла после двух попыток: " + ph.Note)
			j.NextAction = nextAction
			if j.NextAction == "" {
				j.NextAction = "correct the repeated failure recorded in last_gate and blocker; reconcile project_context/lease/jobs before a follow-up goal; no owner journalctl required"
			}
			if strings.Contains(strings.ToLower(ph.Note), "face id") {
				j.NextAction = "admin_session_prepare → admin_session_begin (Face ID); authorize the blocked operation, then reconcile lease/jobs before a follow-up run"
			}
			if err != nil {
				return j, err
			}
			return j, c.save(j)
		}
		passed++
		j.Blocker = ""
		if err := c.save(j); err != nil {
			return j, err
		}
	}
}
