package autonomy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/audit"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/toolstats"
	"mcpshka/internal/workmetrics"
)

var repoSegment = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func journalProject(p string) string {
	rel, ok := strings.CutPrefix(path.Clean(p), "/root/repos/")
	if !ok {
		return ""
	}
	parts := strings.SplitN(rel, "/", 3)
	if !repoSegment.MatchString(parts[0]) {
		return ""
	}
	if parts[0] != "wt" {
		return "/root/repos/" + parts[0]
	}
	if len(parts) >= 2 && repoSegment.MatchString(parts[1]) {
		return workmetrics.ProjectRoot("/root/repos/wt/" + parts[1])
	}
	return ""
}

// MutationProject accepts only a repository path from known routing fields.
func MutationProject(in map[string]any) string {
	for _, k := range []string{"path", "workdir"} {
		if p, _ := in[k].(string); p != "" {
			return journalProject(p)
		}
	}
	return ""
}

func field(v any, name string) string {
	r := reflect.ValueOf(v)
	if r.Kind() == reflect.Pointer && !r.IsNil() {
		r = r.Elem()
	}
	if r.Kind() == reflect.Struct {
		f := r.FieldByName(name)
		if f.Kind() == reflect.String {
			return f.String()
		}
	}
	return ""
}

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

func Track(l *audit.Logger, name string, route func(context.Context) string, run tooldef.Handler) tooldef.Handler {
	return func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
		defer toolstats.BeginHTTPTool(ctx)()
		start := time.Now()
		result, out, err := run(ctx, req, in)
		failed := err != nil || result != nil && result.IsError
		toolstats.Default.Record(name, route(ctx), start, time.Since(start), failed)
		observeCall(ctx, req, l, name, start, in, out, failed)
		return result, out, err
	}
}

// observeCall writes fixed routing metadata for MCP calls. The session ID and
// approval URL are hashed. Request text and tool results stay out of the log.
func observeCall(ctx context.Context, req *mcp.CallToolRequest, l *audit.Logger, name string, at time.Time, in, out any, failed bool) {
	if l == nil {
		return
	}
	if req == nil {
		req = tooldef.InvocationRequest(ctx)
	}
	if req == nil || req.Session == nil || req.Session.ID() == "" {
		return
	}
	details := map[string]any{"session": digest(req.Session.ID())}
	project := journalProject(field(in, "Path"))
	if project == "" {
		project = journalProject(field(in, "Workdir"))
	}
	if strings.HasPrefix(name, "mission_") {
		if p := journalProject(field(out, "Path")); p != "" {
			project = p
		}
		id := field(out, "ID")
		if id == "" {
			id = field(in, "ID")
		}
		if len(id) == 32 && strings.Trim(id, "0123456789abcdef") == "" {
			details["mission"] = id
		}
	}
	if !failed && (name == "admin_session_begin" || name == "approved_task_prepare" || name == "webos_deploy") {
		reply := out
		if name == "webos_deploy" {
			r := reflect.ValueOf(out)
			if r.Kind() == reflect.Struct && r.FieldByName("Task").IsValid() && r.FieldByName("Task").CanInterface() {
				reply = r.FieldByName("Task").Interface()
			}
		}
		state := strings.ToLower(field(reply, "State"))
		if state == "awaiting_face_id" || state == "waiting" || state == "pending" {
			url := field(reply, "ApprovalURL")
			if name == "admin_session_begin" {
				url = field(reply, "URL")
			}
			if url != "" {
				details["approval"] = digest(url)
			}
		}
	}
	status := "ok"
	if failed {
		status = "error"
	}
	_ = l.Log(audit.Event{Time: at.UTC(), Type: workmetrics.SessionCall, Workdir: project, Command: name, Result: status, Details: details})
}
