package processtools

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tooldef"
	"regexp"
	"time"
)

type ProcStartInput struct {
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	Cwd            string   `json:"cwd"`
	PTY            bool     `json:"pty,omitempty"`
	LeaseToken     string   `json:"lease_token,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type ProcInput struct {
	SessionID      string `json:"session_id"`
	Input          string `json:"input,omitempty"`
	Cursor         int64  `json:"cursor,omitempty"`
	Stream         string `json:"stream,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	TimeoutMS      int    `json:"timeout_ms,omitempty"`
	Pattern        string `json:"pattern,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type ProcResult struct {
	SessionID string        `json:"session_id"`
	Job       jobs.Snapshot `json:"job"`
}

func Register(add func(tooldef.Def), m *jobs.Manager, owner func(context.Context) string, route func(string, []string, string) error) {
	RegisterHost(add)
	RegisterHTTPGet(add)
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "proc_start", Description: "Start an interactive local command using exec command/cwd/lease policy and job resource limits. Returns session_id (also job ID); optional Linux PTY merges stderr into stdout. Up to 8 sessions, 10 minute client-idle TTL, 30 minute wall limit, 1 MiB stored output.", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ProcStartInput) (*mcp.CallToolResult, ProcResult, error) {
		if err := route(in.Command, in.Args, in.Cwd); err != nil {
			return nil, ProcResult{}, err
		}
		out, err := m.StartWithOptions(in.Command, in.Args, in.Cwd, jobs.StartOptions{
			InteractiveOwner: owner(ctx), PTY: in.PTY, HolderToken: in.LeaseToken,
			OutputLimitBytes: 1 << 20, Timeout: 30 * time.Minute, MemoryLimitBytes: 128 << 20, ProcessLimit: 16,
		})
		return nil, ProcResult{SessionID: out.ID, Job: out}, err
	}))
	read := func(ctx context.Context, in ProcInput) (jobs.Output, error) {
		if err := m.TouchInteractive(in.SessionID, owner(ctx)); err != nil {
			return jobs.Output{}, err
		}
		var pattern *regexp.Regexp
		if len(in.Pattern) > 512 {
			return jobs.Output{}, errors.New("pattern exceeds 512 bytes")
		}
		if in.Pattern != "" {
			var err error
			pattern, err = regexp.Compile(in.Pattern)
			if err != nil {
				return jobs.Output{}, err
			}
		}
		timeout := time.Duration(max(0, min(in.TimeoutMS, 10000))) * time.Millisecond
		deadline := time.Now().Add(timeout)
		// One audit record per call, not per 25 ms poll.
		var out jobs.Output
		defer func() {
			if logger := m.AuditLogger(); logger != nil {
				_ = logger.Log(audit.Event{Type: "proc_read", JobID: in.SessionID, Result: "observed", Details: map[string]any{"cursor": in.Cursor, "bytes": len(out.Text)}})
			}
		}()
		for {
			var err error
			out, err = m.Read(in.SessionID, in.Stream, in.Cursor, in.Limit)
			if err != nil {
				return out, err
			}
			if (pattern == nil && out.Text != "") || (pattern != nil && pattern.MatchString(out.Text)) ||
				time.Now().After(deadline) || (out.State != jobs.Running && out.State != jobs.Queued) || out.Truncated {
				return out, nil
			}
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	add(tooldef.New(&mcp.Tool{Name: "proc_send", Description: "Send at most 64 KiB to your session's stdin exactly once per retry key. Optionally wait up to 10000 ms for output or an RE2 prompt pattern; bounded output starts at cursor. Never stores stdin in audit.", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ProcInput) (*mcp.CallToolResult, jobs.Output, error) {
		// Validate all read options before input can take effect.
		if in.Cursor < 0 || (in.Stream != "" && in.Stream != "stdout" && in.Stream != "stderr") || len(in.Pattern) > 512 {
			return nil, jobs.Output{}, errors.New("invalid cursor, stream or pattern")
		}
		if _, err := regexp.Compile(in.Pattern); err != nil {
			return nil, jobs.Output{}, err
		}
		if err := m.SendInput(in.SessionID, owner(ctx), in.Input); err != nil {
			return nil, jobs.Output{}, err
		}
		out, err := read(ctx, in)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "proc_read", Description: "Read your process stdout or stderr with byte cursor and at most 64 KiB; optional wait/prompt pattern uses the same bounds as proc_send. PTY output is stdout.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ProcInput) (*mcp.CallToolResult, jobs.Output, error) {
		out, err := read(ctx, in)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "proc_list", Description: "List up to 64 of your interactive process sessions, newest first, including state and job resource counters. Other principals' sessions are excluded.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"sessions": m.InteractiveSessions(owner(ctx))}, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "proc_kill", Description: "Cancel your interactive process session through job group TERM followed by KILL; safe to repeat for a finished session.", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ProcInput) (*mcp.CallToolResult, jobs.Snapshot, error) {
		if err := m.TouchInteractive(in.SessionID, owner(ctx)); err != nil {
			return nil, jobs.Snapshot{}, err
		}
		out, err := m.Cancel(in.SessionID)
		return nil, out, err
	}))
}
