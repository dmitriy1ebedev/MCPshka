package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/providers"
)

// TaskDir holds delegated agent tasks: <id>.prompt written by the MCPshka
// service, <id>.json written by the root task unit.
const TaskDir = "/var/lib/mcpshka/agent-tasks"

var taskID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ValidTaskID reports a well-formed delegated task id.
func ValidTaskID(id string) bool { return taskID.MatchString(id) }

var taskHeavyQueue = heavyqueue.Host()

// Task is the durable record of one delegated agent session.
type Task struct {
	Quota      providers.Quota      `json:"quota"`
	Identity   providers.Identity   `json:"identity"`
	ResumeOf   string               `json:"resume_of,omitempty"`
	ResumedBy  string               `json:"resumed_by,omitempty"`
	ResumeAt   time.Time            `json:"resume_at,omitempty"`
	Queue      *heavyqueue.Status   `json:"queue,omitempty"`
	ID         string               `json:"id"`
	Agent      string               `json:"agent"`
	Path       string               `json:"path"`
	State      string               `json:"state"` // QUEUED, running, done, failed, quota, retry_scheduled, unknown, cancelled
	StartedAt  time.Time            `json:"started_at"`
	FinishedAt time.Time            `json:"finished_at,omitempty"`
	ResetAt    time.Time            `json:"reset_at,omitempty"`
	Tail       string               `json:"tail,omitempty"`
	Error      string               `json:"error,omitempty"`
	NextAction string               `json:"next_action,omitempty"`
	Journal    *Journal             `json:"journal,omitempty"`
	Lease      *TaskLeaseCheckpoint `json:"lease,omitempty"`
}

// MarkInterrupted turns a task the host reboot killed into state
// "interrupted": the record still says running (killed before it could
// write) or failed by a signal shortly before boot.
func MarkInterrupted(t Task, boot time.Time) Task {
	if boot.IsZero() || !t.StartedAt.Before(boot) {
		return t
	}
	killed := (t.State == "running" || t.State == "QUEUED" || ((t.State == "quota" || t.State == "retry_scheduled") && !t.ResumeAt.IsZero())) ||
		(t.State == "failed" && strings.HasPrefix(t.Error, "signal: ") && boot.Sub(t.FinishedAt) < 15*time.Minute)
	if !killed {
		return t
	}
	t.State = "interrupted"
	t.NextAction = "the host rebooted during this task; continue it with agent_task resume_of=" + t.ID + " (same agent and path; prompt optional)"
	return t
}

// CheckResume prevents a second writer while a runner or attempt is alive.
// UNKNOWN is never automatically retried; an explicit continuation first
// proves that its unit ended, then reconciles the worktree through ResumePrompt.
// held reports whether the lease store still holds path for owner; a task
// killed mid-run never recorded its release, but a lease the store no longer
// holds for it is released (13.11: resume refused after a stale release).
func CheckResume(ctx context.Context, t Task, held func(path, owner string) bool) error {
	if t.Lease != nil && t.Lease.State != "released" && t.Lease.State != "not_acquired" && held != nil && !held(t.Path, "agent-task:"+t.ID) {
		t.Lease.State = "released"
	}
	return checkResume(t, func() (bool, error) { return attemptEnded(ctx, t.ID) })
}

func checkResume(t Task, ended func() (bool, error)) error {
	if t.Lease != nil && t.Lease.State != "released" && t.Lease.State != "not_acquired" {
		return errors.New("task lease cleanup unverified: HOLD; reconcile original acquire key " + t.Lease.AcquireKey + " and release key " + t.Lease.ReleaseKey + " before any successor")
	}
	if t.ResumedBy != "" {
		return errors.New("task already resumed; observe agent_task_status id=" + t.ResumedBy)
	}
	switch t.State {
	case "failed", "interrupted", "cancelled":
		return nil
	case "quota", "retry_scheduled":
		if t.ResumeAt.IsZero() {
			return nil
		}
	case "unknown":
		if done, err := ended(); err == nil && done {
			return nil
		}
		return errors.New("attempt unit active or unverified: HOLD; observe agent_task_status id=" + t.ID + " before requesting continuation again")
	}
	return errors.New("task is active, already finished, or scheduled for automatic resume; observe agent_task_status")
}

// ResumePrompt prefixes the interrupted task's prompt so the new session
// picks up the uncommitted work instead of starting over.
func ResumePrompt(prev Task, original, extra string) (string, error) {
	var b strings.Builder
	reason := "прерванной перезагрузкой хоста"
	if prev.State == "quota" {
		reason = "остановленной лимитом квоты"
	} else if prev.State == "retry_scheduled" {
		reason = "остановленной сетевой ошибкой исполнителя"
	} else if prev.State == "unknown" {
		reason = "с неизвестным итогом; до любых изменений сверь фактические эффекты прошлого запуска"
	} else if prev.State == "failed" {
		reason = "остановленной ошибкой"
	} else if prev.State == "cancelled" {
		reason = "отменённой владельцем; сохранённые изменения могут быть неполными"
	}
	fmt.Fprintf(&b, "Это продолжение задачи %s (%s, %s), %s (запуск %s). "+
		"Сначала git status и git diff; продолжай сохранённую работу, не начинай заново и не откатывай сделанное. "+
		"Проверь project_context, project_lease_status и jobs/deploy прошлого запуска. Чужой или неподтверждённый lease не обходи; восстановление только штатными инструментами и под Face ID, если требуется.\n\n",
		prev.ID, prev.Agent, prev.Path, reason, prev.StartedAt.Format(time.RFC3339))
	if strings.TrimSpace(extra) != "" {
		b.WriteString("Дополнение к задаче:\n" + extra + "\n\n")
	}
	b.WriteString("Исходная задача:\n")
	b.WriteString(original)
	if b.Len() > 64<<10 {
		return "", errors.New("resumed prompt exceeds 64 KiB")
	}
	return b.String(), nil
}

// ReadPrompt loads the prompt of an earlier task.
func ReadPrompt(id string) (string, error) {
	if !ValidTaskID(id) {
		return "", errors.New("invalid task id")
	}
	raw, err := os.ReadFile(filepath.Join(taskDir, id+".prompt"))
	return string(raw), err
}

var taskDir = TaskDir

func saveTask(t Task) error {
	raw, err := json.MarshalIndent(t, "", " ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(taskDir, "."+t.ID+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0640); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		if err := f.Chown(994, 984); err != nil { // mcpshka:mcpshka, 994 is the sgx group
			return err
		}
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(taskDir, t.ID+".json")); err != nil {
		return err
	}
	dir, err := os.Open(taskDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// ReadTask loads one task record.
func ReadTask(id string) (Task, error) {
	var t Task
	if !ValidTaskID(id) {
		return t, errors.New("invalid task id")
	}
	raw, err := os.ReadFile(filepath.Join(taskDir, id+".json"))
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(raw, &t)
}

// RunTask executes a delegated prompt with the requested agent and records
// the outcome; run is Run in production.
func RunTask(ctx context.Context, id, agent, path string, run func(ctx context.Context, agent, dir, prompt, report string) (string, error)) (Task, error) {
	return runTask(ctx, id, agent, path, "", false, run)
}

func runTask(ctx context.Context, id, agent, path, resumeOf string, autoResume bool, run taskRun) (Task, error) {
	t := Task{ID: id, Agent: agent, Path: path, ResumeOf: resumeOf, State: "QUEUED", StartedAt: time.Now().UTC()}
	if !ValidTaskID(id) {
		return t, errors.New("invalid task id")
	}
	prompt, err := os.ReadFile(filepath.Join(taskDir, id+".prompt"))
	if err != nil || len(prompt) == 0 || len(prompt) > 64<<10 {
		t.State, t.Error = "failed", "prompt missing or larger than 64 KiB"
		return t, saveTask(t)
	}
	if err := saveTask(t); err != nil {
		return t, err
	}
	ticket, err := taskHeavyQueue.EnqueueScoped("agent:"+id, path)
	if err != nil {
		return t, err
	}
	defer ticket.Close()
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	var statusErr error
	t.State = "QUEUED"
	err = ticket.Wait(waitCtx, func(s heavyqueue.Status) {
		t.Queue = &s
		t.State = s.State
		if s.State == "RUNNING" {
			t.State = "running"
			t.StartedAt = time.Now().UTC()
		}
		if statusErr = saveTask(t); statusErr != nil {
			cancelWait()
		}
	})
	if statusErr != nil {
		return t, statusErr
	}
	if err != nil {
		t.State, t.Error, t.FinishedAt = "failed", err.Error(), time.Now().UTC()
		return t, saveTask(t)
	}
	t.Journal = &Journal{Purpose: purposeOf(string(prompt))}
	t.Journal.HeadBefore, t.Journal.Branch = gitHead(ctx, path)
	runCtx, stopRun := context.WithTimeout(ctx, 3*time.Hour)
	defer stopRun()
	report := filepath.Join(taskDir, id+".report.md")
	runCtx = context.WithValue(runCtx, taskLeaseContextKey{}, &taskLeaseContext{id: id, path: path, report: report, save: func(checkpoint TaskLeaseCheckpoint) error {
		t.Lease = &checkpoint
		return saveTask(t)
	}})
	tail, rerr := run(runCtx, agent, path, string(prompt), report)
	if len(tail) > 8<<10 {
		tail = strings.ToValidUTF8(tail[len(tail)-8<<10:], "?")
	}
	t.Tail, t.FinishedAt, t.State = tail, time.Now().UTC(), "done"
	t.Queue = nil
	finishJournal(t.Journal, ctx, path, tail)
	if errors.Is(rerr, errAttemptUnknown) {
		t.State, t.Error, t.NextAction = "unknown", rerr.Error(), "reconcile the exact attempt unit and project_context/lease/jobs before any manual resume_of="+id
	} else if q, ok := IsQuota(rerr); ok {
		t.State, t.ResetAt, t.Error = "quota", q.Reset, q.Error()
		if autoResume && !q.Reset.IsZero() && q.Reset.After(time.Now()) {
			t.ResumeAt = q.Reset.Add(time.Minute)
			t.NextAction = "runner evaluating quota reset; observe agent_task_status"
		}
	} else if transport := new(TransportError); errors.As(rerr, &transport) {
		t.State, t.Error = "retry_scheduled", rerr.Error()
		t.NextAction = "transport failure; reconcile before manual resume_of=" + id
		if autoResume {
			t.NextAction = "runner evaluating transport retry; observe agent_task_status"
		}
	} else if rerr != nil {
		t.State, t.Error = "failed", rerr.Error()
	}
	return t, saveTask(t)
}

// EndedTaskCovering finds the finished task that held a lease on path: the
// lease was taken while the task ran and its heartbeat stopped by the time
// the task ended. A Codex stopped mid-run left the DIAG lease blocked for two
// hours although its unit was gone. Unit liveness is checked by the caller.
// shortcut: task records are service-writable, so this only shortens the wait
// under the owner's Face ID and next to the process evidence; bind leases to
// tasks when agents get their own MCP identity.
func EndedTaskCovering(path string, acquired, last time.Time) (Task, bool) {
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return Task{}, false
	}
	var found Task
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !ValidTaskID(id) {
			continue
		}
		t, err := ReadTask(id)
		if err != nil || filepath.Clean(t.Path) != path {
			continue
		}
		// A live task on the path may hold it, unless it started after the
		// lease was taken: then it is the successor waiting for this very
		// lease, and blocking on it kept a resumed Codex out (BUG-20261024-91).
		if t.State == "running" {
			if t.StartedAt.After(acquired) {
				continue
			}
			return Task{}, false
		}
		if !t.FinishedAt.IsZero() && !t.StartedAt.After(acquired) && !t.FinishedAt.Before(last) {
			found = t
		}
	}
	return found, found.ID != ""
}
