package roadmaprun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"mcpshka/internal/agents"
)

func withStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	saved := stateDir
	stateDir = dir
	t.Cleanup(func() { stateDir = saved })
	return dir
}

func TestRunClosesPhasesUntilLimitAndJournals(t *testing.T) {
	state := withStateDir(t)
	roadmap := filepath.Join(t.TempDir(), "ROADMAP.md")
	write := func(s string) {
		if err := os.WriteFile(roadmap, []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("## P03 — exec\n- [ ] a\n## P04 — schema\n- [ ] b\n## P05 — x\n- [ ] c\n")
	var prompts []string
	agent := func(ctx context.Context, name, dir, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		raw, _ := os.ReadFile(roadmap)
		// The agent closes the phase it was given.
		for _, id := range []string{"P03", "P04", "P05"} {
			if strings.Contains(prompt, "фазу roadmap: "+id) {
				write(strings.Replace(string(raw), "## "+id+" — ", "## "+id+" — ", 1))
				lines := strings.Split(string(raw), "\n")
				for i, l := range lines {
					if strings.HasPrefix(l, "## "+id) && i+1 < len(lines) {
						lines[i+1] = strings.Replace(lines[i+1], "[ ]", "[x]", 1)
					}
				}
				write(strings.Join(lines, "\n"))
			}
		}
		return "PHASE_DONE", nil
	}
	gate := func(ctx context.Context, path, phase string) (string, string, string, error) {
		return "PASS", strings.Repeat("a", 40), "proceed", nil
	}
	j, err := Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "r1", Path: "/root/repos/diag", Roadmap: roadmap, MaxPhases: 2, Budget: time.Hour, RunAgent: agent, Gate: gate}, "rules")
	if err != nil || j.State != "done" || len(j.Phases) != 2 || j.Phases[0].ID != "P03" || j.Phases[1].ID != "P04" {
		t.Fatalf("run: %+v %v", j, err)
	}
	if !strings.Contains(prompts[0], "phase_gate path=<проект> phase=P03") || !strings.Contains(prompts[0], "rules") {
		t.Fatal("prompt lacks gate step or rules", prompts[0])
	}
	raw, err := os.ReadFile(filepath.Join(state, "r1.json"))
	var saved Journal
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.State != "done" {
		t.Fatal("journal not persisted", err)
	}
}

func TestRunStopsOnRepeatedBlockAndQuota(t *testing.T) {
	withStateDir(t)
	roadmap := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(roadmap, []byte("## P07 — hard\n- [ ] x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	agent := func(ctx context.Context, name, dir, prompt string) (string, error) {
		calls++
		if calls == 2 && !strings.Contains(prompt, "Прошлая попытка") {
			t.Fatal("retry did not carry the blocker")
		}
		return "BLOCKED: нужен Face ID", nil
	}
	gate := func(ctx context.Context, path, phase string) (string, string, string, error) {
		return "BLOCKED", "", "sealed_test: missing", nil
	}
	j, _ := Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "r2", Path: "/p", Roadmap: roadmap, MaxPhases: 5, Budget: time.Hour, RunAgent: agent, Gate: gate}, "")
	if j.State != "stopped" || calls != 2 || !strings.Contains(j.StopReason, "P07") || !strings.Contains(j.Phases[0].Note, "Face ID") {
		t.Fatalf("blocked run: %+v calls=%d", j, calls)
	}
	quota := func(ctx context.Context, name, dir, prompt string) (string, error) {
		return "usage limit", &agents.QuotaError{Agent: name, Reset: time.Now().Add(48 * time.Hour)}
	}
	j, _ = Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "r3", Path: "/p", Roadmap: roadmap, MaxPhases: 5, Budget: time.Hour, RunAgent: quota, Gate: gate}, "")
	if j.State != "stopped" || !strings.Contains(j.StopReason, "лимит") {
		t.Fatalf("quota run: %+v", j)
	}
}

func TestRunSwitchesAgentOnQuotaAndWaitsWhenAllOut(t *testing.T) {
	withStateDir(t)
	roadmap := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(roadmap, []byte("## P09 — x\n- [ ] a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 21, 0, 0, 0, time.UTC)
	var used []string
	run := func(ctx context.Context, name, dir, prompt string) (string, error) {
		used = append(used, name)
		if name == agents.Codex {
			return "", &agents.QuotaError{Agent: name, Reset: now.Add(time.Hour)}
		}
		_ = os.WriteFile(roadmap, []byte("## P09 — x\n- [x] a\n"), 0644)
		return "PHASE_DONE P09", nil
	}
	gate := func(ctx context.Context, path, phase string) (string, string, string, error) {
		return "PASS", "h", "", nil
	}
	j, _ := Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "r4", Path: "/p", Roadmap: roadmap, MaxPhases: 1, Budget: 4 * time.Hour, RunAgent: run, Gate: gate, Now: func() time.Time { return now }}, "")
	if j.State != "done" || strings.Join(used, ",") != "codex,claude" || j.Phases[0].Agent != agents.Claude {
		t.Fatalf("switch: %+v used=%v", j, used)
	}
	// Both out: wait for the earliest reset, then continue.
	if err := os.WriteFile(roadmap, []byte("## P10 — y\n- [ ] b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	slept := time.Duration(0)
	calls := 0
	run2 := func(ctx context.Context, name, dir, prompt string) (string, error) {
		calls++
		if calls <= 2 {
			return "", &agents.QuotaError{Agent: name, Reset: now.Add(30 * time.Minute)}
		}
		_ = os.WriteFile(roadmap, []byte("## P10 — y\n- [x] b\n"), 0644)
		return "", nil
	}
	sleep := func(ctx context.Context, d time.Duration) error { slept += d; now = now.Add(d); return nil }
	j, _ = Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "r5", Path: "/p", Roadmap: roadmap, MaxPhases: 1, Budget: 4 * time.Hour, RunAgent: run2, Gate: gate, Now: func() time.Time { return now }, Sleep: sleep}, "")
	if j.State != "done" || slept < 30*time.Minute {
		t.Fatalf("wait: %+v slept=%v", j, slept)
	}
}

type fakeLease struct {
	acquired, released int
	owner              string
	fail               bool
}

func (f *fakeLease) Acquire(ctx context.Context, path, owner, key string) (string, error) {
	if f.fail {
		return "", context.DeadlineExceeded
	}
	f.acquired++
	f.owner = owner
	return "tok-123", nil
}
func (f *fakeLease) Heartbeat(ctx context.Context, path, token, key string) error { return nil }
func (f *fakeLease) Release(ctx context.Context, path, token, key string) error {
	f.released++
	return nil
}

func TestRunnerHoldsAndLendsTheLease(t *testing.T) {
	withStateDir(t)
	roadmap := filepath.Join(t.TempDir(), "ROADMAP.md")
	if err := os.WriteFile(roadmap, []byte("## P11 — z\n- [ ] a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var prompt string
	run := func(ctx context.Context, name, dir, p string) (string, error) {
		prompt = p
		_ = os.WriteFile(roadmap, []byte("## P11 — z\n- [x] a\n"), 0644)
		return "", nil
	}
	gate := func(ctx context.Context, path, phase string) (string, string, string, error) {
		return "PASS", "h", "", nil
	}
	lease := &fakeLease{}
	j, _ := Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "r6", Path: "/p", Roadmap: roadmap, MaxPhases: 1, Budget: time.Hour, RunAgent: run, Gate: gate, Lease: lease}, "")
	if j.State != "done" || lease.acquired != 1 || lease.released != 1 || lease.owner != "roadmap-run:r6" {
		t.Fatalf("lease not held for the run: %+v %+v", j, lease)
	}
	if !strings.Contains(prompt, "lease_token=tok-123") || !strings.Contains(prompt, "НЕ вызывай project_lease_acquire") {
		t.Fatal("agent not given the runner's lease", prompt)
	}
	if err := os.WriteFile(roadmap, []byte("## P12 — z\n- [ ] a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	j, _ = Run(context.Background(), Config{Agents: []string{"codex", "claude"}, ID: "r7", Path: "/p", Roadmap: roadmap, MaxPhases: 1, Budget: time.Hour, RunAgent: run, Gate: gate, Lease: &fakeLease{fail: true}}, "")
	if j.State != "stopped" || !strings.Contains(j.StopReason, "lease") {
		t.Fatalf("busy lease not reported: %+v", j)
	}
}

// BUG-20261021-33: quota waits preserve the phase, attempts and run budget.
func TestQuotaWaitBudgetAndJournal(t *testing.T) {
	for _, tc := range []struct {
		name              string
		resets            []time.Duration // zero means unknown
		budget            time.Duration
		cancel, oversleep bool
		wantSleep         time.Duration
		wantState, reason string
	}{
		{name: "known reset", resets: []time.Duration{10 * time.Minute}, budget: time.Hour, wantSleep: 11 * time.Minute, wantState: "done"},
		{name: "stale reset", resets: []time.Duration{-2 * time.Minute}, budget: time.Hour, wantState: "stopped", reason: "неизвестно"},
		{name: "unknown reset", resets: []time.Duration{0}, budget: 2 * time.Hour, wantState: "stopped", reason: "неизвестно"},
		{name: "reset beyond budget", resets: []time.Duration{2 * time.Hour}, budget: time.Hour, wantState: "stopped", reason: "бюджет"},
		{name: "buffer beyond budget", resets: []time.Duration{time.Hour - 30*time.Second}, budget: time.Hour, wantState: "stopped", reason: "бюджет"},
		{name: "buffer at deadline", resets: []time.Duration{time.Hour - time.Minute}, budget: time.Hour, wantState: "stopped", reason: "бюджет"},
		{name: "unknown and known", resets: []time.Duration{0, 10 * time.Minute}, budget: time.Hour, wantSleep: 11 * time.Minute, wantState: "done"},
		{name: "cancel wait", resets: []time.Duration{10 * time.Minute}, budget: time.Hour, cancel: true, wantSleep: 11 * time.Minute, wantState: "stopped", reason: "прерван"},
		{name: "late wakeup", resets: []time.Duration{10 * time.Minute}, budget: time.Hour, oversleep: true, wantSleep: 11 * time.Minute, wantState: "stopped", reason: "бюджет"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := withStateDir(t)
			roadmap := filepath.Join(state, "ROADMAP.md")
			if err := os.WriteFile(roadmap, []byte("## P33 — quota\n- [ ] work\n"), 0600); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			start := now
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, gates := 0, 0
			var slept time.Duration
			cfg := Config{ID: "quota", Roadmap: roadmap, MaxPhases: 1, Budget: tc.budget, Agents: agents.Known[:len(tc.resets)], Now: func() time.Time { return now }}
			cfg.RunAgent = func(ctx context.Context, name, dir, prompt string) (string, error) {
				calls++
				if calls <= len(tc.resets) {
					var reset time.Time
					if d := tc.resets[calls-1]; d != 0 {
						reset = start.Add(d)
					}
					return "", &agents.QuotaError{Agent: name, Reset: reset}
				}
				if ctx.Err() != nil {
					t.Fatalf("agent received expired context: %v", ctx.Err())
				}
				if !strings.Contains(prompt, "фазу roadmap: P33") {
					t.Fatal("phase changed", prompt)
				}
				return "PHASE_DONE P33", nil
			}
			cfg.Gate = func(context.Context, string, string) (string, string, string, error) {
				gates++
				return "PASS", "head", "", nil
			}
			cfg.Sleep = func(ctx context.Context, d time.Duration) error {
				slept += d
				if slept > tc.budget {
					t.Fatal("repeated or unbounded wait")
				}
				raw, err := os.ReadFile(filepath.Join(state, "quota.json"))
				var saved Journal
				if err != nil || json.Unmarshal(raw, &saved) != nil || len(saved.Phases) != 1 {
					t.Fatalf("missing waiting phase: %s (%v)", raw, err)
				}
				if saved.Phases[0].Attempts != 0 || !strings.Contains(saved.Phases[0].Note, "ждём") {
					t.Fatalf("waiting phase: %+v", saved.Phases[0])
				}
				if tc.cancel {
					cancel()
					return ctx.Err()
				}
				now = now.Add(d)
				if tc.oversleep {
					now = start.Add(tc.budget)
				}
				return nil
			}
			j, err := Run(ctx, cfg, "")
			if err != nil || j.State != tc.wantState || !strings.Contains(j.StopReason, tc.reason) || slept != tc.wantSleep {
				t.Fatalf("journal=%+v err=%v slept=%v", j, err, slept)
			}
			if len(j.Phases) != 1 {
				t.Fatalf("lost phase: %+v", j)
			}
			wantAttempts := 0
			if tc.wantState == "done" {
				wantAttempts = 1
			}
			if tc.wantSleep > 0 && len(j.Phases[0].QuotaWaits) != 1 {
				t.Fatalf("wait history lost: %+v", j.Phases[0])
			}
			if j.Phases[0].Attempts != wantAttempts || gates != wantAttempts || calls != len(tc.resets)+wantAttempts {
				t.Fatalf("phase=%+v calls=%d gates=%d", j.Phases[0], calls, gates)
			}
		})
	}
}

type quotaLease struct {
	fakeLease
	beats atomic.Int32
}

func (l *quotaLease) Heartbeat(context.Context, string, string, string) error {
	l.beats.Add(1)
	return nil
}

func TestQuotaWaitKeepsLeaseHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := withStateDir(t)
		roadmap := filepath.Join(state, "ROADMAP.md")
		if err := os.WriteFile(roadmap, []byte("## P33 — quota\n- [ ] work\n"), 0600); err != nil {
			t.Fatal(err)
		}
		lease := &quotaLease{}
		calls := 0
		start := time.Now()
		j, err := Run(context.Background(), Config{
			ID: "heartbeat", Roadmap: roadmap, MaxPhases: 1, Budget: time.Hour,
			Agents: []string{agents.Claude}, Lease: lease,
			RunAgent: func(ctx context.Context, name, dir, prompt string) (string, error) {
				calls++
				if calls == 1 {
					return "", &agents.QuotaError{Agent: name, Reset: start.Add(2 * time.Minute)}
				}
				if lease.beats.Load() < 2 || lease.released != 0 {
					t.Fatal("lease not maintained during quota wait")
				}
				return "", nil
			},
			Gate: func(context.Context, string, string) (string, string, string, error) { return "PASS", "head", "", nil },
		}, "")
		if err != nil || j.State != "done" || calls != 2 || j.Phases[0].Attempts != 1 || time.Since(start) != 3*time.Minute || lease.released != 1 {
			t.Fatalf("journal=%+v err=%v calls=%d elapsed=%v", j, err, calls, time.Since(start))
		}
	})
}

func TestQuotaFallbackPreservesRemainingAttempt(t *testing.T) {
	for _, order := range [][]string{{agents.Codex, agents.Claude}, {agents.Claude, agents.Codex}} {
		t.Run(order[0], func(t *testing.T) {
			state := withStateDir(t)
			roadmap := filepath.Join(state, "ROADMAP.md")
			if err := os.WriteFile(roadmap, []byte("## P33 — quota\n- [ ] work\n"), 0600); err != nil {
				t.Fatal(err)
			}
			calls, gates := 0, 0
			j, err := Run(context.Background(), Config{
				ID: "fallback", Roadmap: roadmap, MaxPhases: 1, Budget: time.Hour, Agents: order,
				RunAgent: func(ctx context.Context, name, dir, prompt string) (string, error) {
					calls++
					if calls == 2 {
						return "", &agents.QuotaError{Agent: name}
					}
					if calls == 3 && (name != order[1] || !strings.Contains(prompt, "repair needed")) {
						t.Fatal("fallback lost agent or blocker", name, prompt)
					}
					return "", nil
				},
				Sleep: func(context.Context, time.Duration) error { t.Fatal("available fallback must not wait"); return nil },
				Gate: func(context.Context, string, string) (string, string, string, error) {
					gates++
					if gates == 1 {
						return "BLOCKED", "", "repair needed", nil
					}
					return "PASS", "head", "", nil
				},
			}, "")
			if err != nil || j.State != "done" || calls != 3 || gates != 2 || j.Phases[0].Attempts != 2 {
				t.Fatalf("journal=%+v err=%v calls=%d gates=%d", j, err, calls, gates)
			}
		})
	}
}

func TestMarkInterruptedAfterReboot(t *testing.T) {
	boot := time.Date(2026, 10, 22, 17, 16, 0, 0, time.UTC)
	j := Journal{State: "running", UpdatedAt: boot.Add(-time.Minute)}
	if got := MarkInterrupted(j, boot); got.State != "interrupted" || got.StopReason == "" {
		t.Fatal("run killed by reboot still running", got)
	}
	for _, j := range []Journal{{State: "running", UpdatedAt: boot.Add(time.Minute)}, {State: "stopped", UpdatedAt: boot.Add(-time.Hour)}} {
		if MarkInterrupted(j, boot).State == "interrupted" {
			t.Fatal("wrongly interrupted", j)
		}
	}
}
