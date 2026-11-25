// Package budget decides how long one MCPshka action may run — one rule for
// every job path (exec_start, project_test/run/build, sealed recipes, CI).
//
// Owner, 03.11.2026: «не просто лимит, а гибкое правило». The rule follows
// common practice: Temporal (start-to-close + heartbeat + schedule-to-close),
// CircleCI no_output_timeout, Buildkite runtime timeout extension, and the
// p95-of-history budgets used with GitHub/GitLab CI:
//
//   - wall budget: an explicit request wins, but never below what the same
//     action needed before (p95 of recent successes × 2 + 2 min); without a
//     request, history, else the kind default;
//   - H96 load stretches the budget (other work slows this one);
//   - a stall watchdog ends a job that shows no progress (no output, no CPU)
//     for Idle, long before the wall budget;
//   - a job still progressing at its budget is extended in steps up to Ceiling;
//   - one Ceiling for all layers; anything above is clamped and said so.
package budget

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// Ceiling is the one hard bound for every job layer (jobs, executor,
	// Workbench, root broker recipes, pipelines).
	Ceiling = 6 * time.Hour
	// CeilingSeconds is Ceiling for integer-second contracts.
	CeilingSeconds = int64(Ceiling / time.Second)
	// Floor is the smallest budget the rule produces from history.
	Floor = 2 * time.Minute
	// MinIdle is the shortest stall window; shorter jobs rely on the wall budget.
	MinIdle = 20 * time.Minute
	// MaxIdle bounds the stall window of very long jobs.
	MaxIdle = time.Hour
	// History is how many recent successes the rule reads.
	History = 30
)

// Decision is the budget for one start, explained for the caller.
type Decision struct {
	Wall    time.Duration // wall-clock budget before an extension check
	Idle    time.Duration // stall window (0: none, budget too short to need one)
	Ceiling time.Duration
	P95     time.Duration // p95 of recent successes (0: no history)
	Samples int
	Source  string // requested, raised, history, default, clamped
	Note    string
}

// Input is what the caller knows at start.
type Input struct {
	Requested time.Duration   // 0: not set by the caller
	Default   time.Duration   // kind default when nothing else is known
	History   []time.Duration // durations of recent successful runs, any order
	Load      float64         // 1-minute load average (0: unknown)
	CPUs      int             // online CPUs (0: unknown)
}

// P95 returns the 95th percentile (nearest rank) of d.
func P95(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(math.Ceil(0.95*float64(len(s)))) - 1
	return s[max(rank, 0)]
}

// LoadFactor stretches budgets on a busy host: 1 at or below one runnable
// task per CPU, up to 2 when the host is twice oversubscribed.
func LoadFactor(load float64, cpus int) float64 {
	if load <= 0 || cpus <= 0 {
		return 1
	}
	return math.Min(math.Max(load/float64(cpus), 1), 2)
}

// HostLoad is the 1-minute load average (0 when unreadable).
func HostLoad() float64 {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// Phase is the budget of a short bounded phase without a progress signal
// (snapshot preparation, a probe): its default stretched by the current H96
// load, so never more than PhaseMax(def). 08.11: a fixed 90 s preparation
// expired at load 5 and failed a full test run that only had to wait.
func Phase(def time.Duration) time.Duration {
	return time.Duration(float64(def) * LoadFactor(HostLoad(), runtime.NumCPU()))
}

// PhaseMax bounds Phase(def) for fences (unit lifetimes, observer windows).
func PhaseMax(def time.Duration) time.Duration { return 2 * def }

// Decide applies the rule.
func Decide(in Input) Decision {
	if len(in.History) > History {
		in.History = in.History[len(in.History)-History:]
	}
	d := Decision{Ceiling: Ceiling, P95: P95(in.History), Samples: len(in.History)}
	need := time.Duration(0)
	if d.P95 > 0 {
		need = max(Floor, 2*d.P95+2*time.Minute)
	}
	f := LoadFactor(in.Load, in.CPUs)
	stretch := func(x time.Duration) time.Duration { return time.Duration(float64(x) * f) }
	switch {
	case in.Requested > 0 && need > 0 && in.Requested < stretch(need):
		d.Wall, d.Source = stretch(need), "raised"
		d.Note = fmt.Sprintf("запрошено %s, но такие прогоны шли до %s (p95 из %d)", round(in.Requested), round(d.P95), d.Samples)
	case in.Requested > 0:
		d.Wall, d.Source = in.Requested, "requested"
	case need > 0:
		d.Wall, d.Source = stretch(need), "history"
	default:
		d.Wall, d.Source = stretch(in.Default), "default"
	}
	if f > 1 && d.Source != "requested" {
		d.Note = join(d.Note, fmt.Sprintf("нагрузка H96 ×%.1f", f))
	}
	if d.Wall > Ceiling {
		d.Note = join(d.Note, fmt.Sprintf("обрезано до потолка %s", round(Ceiling)))
		d.Wall, d.Source = Ceiling, "clamped"
	}
	if d.Wall >= MinIdle {
		d.Idle = min(max(MinIdle, d.Wall/3), MaxIdle)
	}
	return d
}

// Extension is how much to add when a job reaches its wall budget while still
// progressing: half the budget, at least 10 min, never past Ceiling from start;
// none for budgets under MinIdle.
func Extension(started, deadline, now time.Time) time.Duration {
	budget := deadline.Sub(started)
	if budget < MinIdle {
		return 0 // short jobs end at their budget; extension is for long ones
	}
	add := max(budget/2, 10*time.Minute)
	if limit := started.Add(Ceiling); now.Add(add).After(limit) {
		add = limit.Sub(now)
	}
	return max(add, 0)
}

// String is the one-line explanation returned to callers.
func (d Decision) String() string {
	s := fmt.Sprintf("бюджет %s (%s)", round(d.Wall), map[string]string{"requested": "как запрошено", "raised": "поднят по истории", "history": "по истории", "default": "по умолчанию", "clamped": "потолок"}[d.Source])
	if d.P95 > 0 {
		s += fmt.Sprintf(", p95 %s из %d", round(d.P95), d.Samples)
	}
	if d.Idle > 0 {
		s += fmt.Sprintf(", сторож тишины %s", round(d.Idle))
	}
	s += fmt.Sprintf(", продление при прогрессе до %s", round(d.Ceiling))
	if d.Note != "" {
		s += "; " + d.Note
	}
	return s
}

func round(d time.Duration) time.Duration {
	if d >= time.Minute {
		return d.Round(time.Minute)
	}
	return d.Round(time.Second)
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
