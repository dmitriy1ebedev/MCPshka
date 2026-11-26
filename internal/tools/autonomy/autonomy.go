package autonomy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/agents"
	"mcpshka/internal/failure"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/workmetrics"
)

type Window struct {
	Path         string `json:"path,omitempty" jsonschema:"Allowed Git project or worktree. Empty measures every project."`
	Since        string `json:"since,omitempty" jsonschema:"Inclusive RFC3339 start. Default is seven days before until."`
	Until        string `json:"until,omitempty" jsonschema:"Exclusive RFC3339 end. Default is now."`
	StuckSeconds int    `json:"stuck_seconds,omitempty" jsonschema:"Minimum age in seconds of an unresolved operation. Default 900."`
}

type ReadInput struct {
	Window
	BaselineDay string `json:"baseline_day,omitempty" jsonschema:"UTC date (YYYY-MM-DD) to read a saved baseline for this project. Omit since/until/stuck_seconds."`
}

type SaveInput struct {
	Window
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Stable retry key. The first snapshot of each project and UTC day is retained."`
}

type Result struct {
	BaselineDay string               `json:"baseline_day,omitempty"`
	Metrics     workmetrics.Autonomy `json:"metrics"`
}

func Sources(m *jobs.Manager) workmetrics.Sources {
	return workmetrics.Sources{DB: m.Database(), Audit: m.AuditLogger(), MemoryDir: m.StateDir(), AgentDir: agents.TaskDir}
}

func Register(add func(tooldef.Def), m *jobs.Manager) {
	s := Sources(m)
	git := gitops.New(m.Policy())
	add(tooldef.New(&mcp.Tool{
		Name:        "autonomy_metrics",
		Description: "Bounded autonomy aggregates over a period and optional project, or read a dated baseline. Reports sources and missing coverage; acceptance uses mission checkpoints, session counts use observed calls, host load is sampled now.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ReadInput) (*mcp.CallToolResult, Result, error) {
		now := time.Now().UTC()
		p, err := in.Window.period(git, now)
		if err != nil {
			return nil, Result{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		if in.BaselineDay == "" {
			return nil, Result{Metrics: s.Measure(ctx, p, now)}, nil
		}
		if in.Since != "" || in.Until != "" || in.StuckSeconds != 0 {
			return nil, Result{}, errors.New("baseline_day cannot be combined with period parameters")
		}
		day, err := time.Parse(time.DateOnly, in.BaselineDay)
		if err != nil {
			return nil, Result{}, errors.New("baseline_day must be YYYY-MM-DD")
		}
		if s.DB == nil {
			return nil, Result{}, errors.New("baseline requires SQLite")
		}
		raw, err := s.DB.GetContext(ctx, workmetrics.BaselineKey(p.Project, day))
		var out Result
		if err == nil {
			err = json.Unmarshal(raw, &out)
		}
		if err == nil && (out.BaselineDay != in.BaselineDay || out.Metrics.Project != p.Project) {
			err = errors.New("baseline identity mismatch")
		}
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{
		Name:        "autonomy_baseline_save",
		Description: "Save the first measured autonomy snapshot for the optional project and current UTC date. Repeat calls return the original immutable snapshot. Read it with autonomy_metrics baseline_day.",
		Annotations: &mcp.ToolAnnotations{},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SaveInput) (*mcp.CallToolResult, Result, error) {
		now := time.Now().UTC()
		p, err := in.Window.period(git, now)
		if err != nil {
			return nil, Result{}, failure.Rejected("INVALID_AUTONOMY_PERIOD", "autonomy.baseline", err.Error(), nil)
		}
		if s.DB == nil {
			return nil, Result{}, failure.Rejected("SQLITE_UNAVAILABLE", "autonomy.baseline", "baseline requires SQLite", nil)
		}
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		out, err := save(ctx, s, p, now)
		return nil, out, err
	}))
}

func save(ctx context.Context, s workmetrics.Sources, p workmetrics.Period, now time.Time) (Result, error) {
	key := workmetrics.BaselineKey(p.Project, now)
	day := now.Format(time.DateOnly)
	out := Result{BaselineDay: day, Metrics: s.Measure(ctx, p, now)}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return Result{}, err
	}
	created, err := s.DB.PutIfAbsent(key, raw)
	if err != nil || created {
		return out, err
	}
	raw, err = s.DB.GetContext(ctx, key)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	if err == nil && (out.BaselineDay != day || out.Metrics.Project != p.Project) {
		err = errors.New("baseline identity mismatch")
	}
	return out, err
}

func (in Window) period(git *gitops.Service, now time.Time) (workmetrics.Period, error) {
	p := workmetrics.Period{Until: now, StuckSeconds: in.StuckSeconds}
	var err error
	if in.Path != "" {
		p.Project, err = git.RepoRoot(in.Path)
		if err != nil {
			return p, err
		}
		p.Project = workmetrics.ProjectRoot(p.Project)
	}
	if in.Until != "" {
		p.Until, err = time.Parse(time.RFC3339, in.Until)
		if err != nil {
			return p, fmt.Errorf("invalid until: %w", err)
		}
		p.Until = p.Until.UTC()
	}
	p.Since = p.Until.Add(-7 * 24 * time.Hour)
	if in.Since != "" {
		p.Since, err = time.Parse(time.RFC3339, in.Since)
		if err != nil {
			return p, fmt.Errorf("invalid since: %w", err)
		}
		p.Since = p.Since.UTC()
	}
	if !p.Since.Before(p.Until) || p.Until.After(now) {
		return p, errors.New("require since < until <= now")
	}
	if p.StuckSeconds == 0 {
		p.StuckSeconds = int(workmetrics.StuckAfter / time.Second)
	}
	if p.StuckSeconds < 0 || p.StuckSeconds > 365*24*3600 {
		return p, errors.New("stuck_seconds must be between 1 and 31536000")
	}
	return p, nil
}
