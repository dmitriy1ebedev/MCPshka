package forgeops

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/toolforge"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ToolForgeInput struct {
	Action         string               `json:"action" jsonschema:"audit, list, compose, remove, request or build"`
	RequestID      string               `json:"request_id,omitempty" jsonschema:"build: id of a recorded request"`
	Agent          string               `json:"agent,omitempty" jsonschema:"build: empty = this chat builds it itself through MCPshka tools (default); codex or claude only when the owner named that agent"`
	Composite      *toolforge.Composite `json:"composite,omitempty" jsonschema:"compose: x_name, description, inputs, steps [{tool, args with {{input.k}} / {{steps.N.k}}}]"`
	Name           string               `json:"name,omitempty" jsonschema:"remove: x_ composite name"`
	Request        *toolforge.Request   `json:"request,omitempty" jsonschema:"request: goal, missing, acceptance for a capability that needs new code"`
	SessionID      string               `json:"admin_session_id,omitempty" jsonschema:"compose/remove/build: active Face ID session"`
	IdempotencyKey string               `json:"idempotency_key,omitempty"`
}

type ToolForgeResult struct {
	Findings   []toolforge.Finding   `json:"findings,omitempty"`
	Omitted    int                   `json:"findings_omitted,omitempty" jsonschema:"findings left out to stay within the mutation output budget"`
	Report     *toolforge.Report     `json:"report,omitempty"` // audit: the catalog in numbers
	Composites []toolforge.Composite `json:"composites,omitempty"`
	Done       string                `json:"done,omitempty"`
	Next       string                `json:"next,omitempty"`
}

// DefaultAuditPath mirrors the server's audit log location.
func DefaultAuditPath() string {
	if p := os.Getenv("MCPSHKA_AUDIT_PATH"); p != "" {
		return p
	}
	state := os.Getenv("MCPSHKA_STATE_DIR")
	if state == "" {
		state = "/var/lib/mcpshka/jobs"
	}
	return filepath.Join(filepath.Dir(state), "audit.jsonl")
}

// AuditCalls counts audited calls per tool (mutations and job/lease events)
// over the live log and its newest rotations: the live file alone covers a
// few hours and called tools looked unused.
func AuditCalls(path string) map[string]int {
	out := map[string]int{}
	files, _ := filepath.Glob(path + ".*")
	sort.Slice(files, func(i, j int) bool { return files[i] > files[j] }) // rotations are named by time
	if len(files) > 8 {
		files = files[:8]
	}
	for _, name := range append([]string{path}, files...) {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e struct {
				Type    string `json:"type"`
				Details struct {
					Tool string `json:"tool"`
				} `json:"details"`
			}
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				out[e.Type]++
				if e.Details.Tool != "" {
					out[e.Details.Tool]++
				}
			}
		}
		f.Close()
	}
	return out
}

func Register(add func(tooldef.Def), d Deps, auditPath string) {
	if list, err := toolforge.Load(); err == nil {
		for _, c := range list {
			d.RegisterComposite(c)
		}
	}
	add(tooldef.New(&mcp.Tool{Name: "tool_forge", Description: "Improve MCPshka's own toolset. audit: report (the catalog in numbers: registered, listed, hidden, deprecated duplicates with replacements, contracts, readiness states, unused mutations, missing capabilities from open requests) and findings (overlapping tools to merge, mutations without contracts, tools without readiness, unused mutations). list: approved composites. compose (Face ID): register a composite tool x_* chaining existing tools with {{input.k}}/{{steps.N.k}} arguments — available immediately, every step keeps its own gates. remove (Face ID): drop a composite. request: record a capability that needs new code (goal, missing, acceptance) for an agent and a release. Use when tool_resolve answers CAPABILITY_MISSING.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in ToolForgeInput) (*mcp.CallToolResult, ToolForgeResult, error) {
		var out ToolForgeResult
		switch in.Action {
		case "audit":
			var tools, live []toolforge.Tool
			for _, t := range d.Catalog(auditPath) {
				tools = append(tools, t)
				// A superseded tool is a recorded duplicate already: no overlap finding about it.
				if _, dup := d.Superseded[t.Name]; !dup {
					live = append(live, t)
				}
			}
			out.Findings, out.Omitted = boundFindings(toolforge.Audit(live))
			report := toolforge.Summarize(tools, d.Hidden, func(n string) string { return d.Readiness(n) }, d.Superseded, toolforge.Requests())
			out.Report = &report
		case "list":
			out.Composites, _ = toolforge.Load()
		case "compose", "remove":
			if err := d.RequireAdmin(ctx, in.SessionID); err != nil {
				return nil, out, err
			}
			if in.Action == "remove" {
				if err := toolforge.Remove(in.Name); err != nil {
					return nil, out, err
				}
				out.Done, out.Next = "removed "+in.Name, "the tool disappears after the next MCPshka restart"
				break
			}
			if in.Composite == nil {
				return nil, out, errors.New("compose needs a composite")
			}
			c, err := toolforge.Validate(*in.Composite, d.Catalog(auditPath))
			if err != nil {
				return nil, out, err
			}
			c.ApprovedAt, c.Session = time.Now().UTC(), in.SessionID
			if err := toolforge.Save(c); err != nil {
				return nil, out, err
			}
			d.RegisterComposite(c)
			out.Done, out.Next = "registered "+c.Name, "refresh the connector or call it through tool_invoke"
		case "request":
			if in.Request == nil {
				return nil, out, errors.New("request needs goal, missing and acceptance")
			}
			rq := *in.Request
			sum := sha256.Sum256([]byte(rq.Goal + rq.Missing))
			rq.ID, rq.At, rq.State = hex.EncodeToString(sum[:8]), time.Now().UTC(), "open"
			if err := toolforge.SaveRequest(rq); err != nil {
				return nil, out, err
			}
			out.Done, out.Next = "request "+rq.ID+" recorded", "tool_forge action=build request_id="+rq.ID+" admin_session_id=<Face ID>: you build it yourself in this chat (mcpshka_guide topic=forge)"
		case "build":
			done, next, err := build(ctx, in, d.Call)
			if err != nil {
				return nil, out, err
			}
			out.Done, out.Next = done, next
		default:
			return nil, out, errors.New("action must be audit, list, compose, remove, request or build")
		}
		return nil, out, nil
	}))
}

// build starts work on a recorded request. By default the chat that
// asked builds it itself through MCPshka tools: it gets the forge worktree
// on a fresh branch from main and a lease. An agent is used only when the
// owner named one (owner 26.10).
func build(ctx context.Context, in ToolForgeInput, call func(context.Context, string, map[string]any) (map[string]any, error)) (string, string, error) {
	rq, err := toolforge.LoadRequest(in.RequestID)
	if err != nil {
		return "", "", fmt.Errorf("unknown request: %w", err)
	}
	if in.SessionID == "" || in.IdempotencyKey == "" {
		return "", "", errors.New("build needs admin_session_id (Face ID window) and idempotency_key")
	}
	branch := "forge/" + rq.ID
	switch in.Agent {
	case "":
	case "codex", "claude":
		prompt := "MCPshka capability request " + rq.ID + ". Goal: " + rq.Goal + "\nMissing: " + rq.Missing + "\nAcceptance: " + rq.Acceptance +
			"\nWork in " + toolforge.ForgeWorktree + " on a new branch " + branch + " from main (git switch -c as the repo owner). Read mcpshka_guide topic=rules and topic=forge first. Logic in internal/<module>, mcpserver only registers. Tests with every change, project_test PASS, commit; do not bump the version, ship or release. Report SHA and tests."
		res, err := call(ctx, "agent_task", map[string]any{"admin_session_id": in.SessionID, "agent": in.Agent, "path": toolforge.ForgeWorktree, "prompt": prompt, "idempotency_key": in.IdempotencyKey})
		if err != nil {
			return "", "", err
		}
		rq.State = "assigned"
		_ = toolforge.SaveRequest(rq)
		return fmt.Sprintf("request %s assigned to %s (task %v)", rq.ID, in.Agent, res["id"]), "agent_task_status, then review, integration_ship and release", nil
	default:
		return "", "", fmt.Errorf("agent %q is not connected; available: codex, claude, or leave empty to build it in this chat", in.Agent)
	}
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-c", "safe.directory=*", "-C", toolforge.ForgeWorktree}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1"}
		raw, err := cmd.Output()
		return strings.TrimSpace(string(raw)), err
	}
	if dirty, err := git("status", "--porcelain=v1", "--untracked-files=no"); err != nil || dirty != "" {
		return "", "", errors.New("forge worktree has uncommitted work of another build: finish or commit it first")
	}
	if _, err := git("switch", "-C", branch, "main"); err != nil {
		return "", "", fmt.Errorf("prepare branch %s: %w", branch, err)
	}
	lease, err := call(ctx, "project_lease_acquire", map[string]any{"path": toolforge.ForgeWorktree, "owner": "forge-" + rq.ID, "idempotency_key": in.IdempotencyKey})
	if err != nil {
		return "", "", fmt.Errorf("forge worktree is busy: %w", err)
	}
	rq.State = "building"
	_ = toolforge.SaveRequest(rq)
	return fmt.Sprintf("build %s yourself in %s on branch %s (lease token %v)", rq.ID, toolforge.ForgeWorktree, branch, lease["token"]),
		"mcpshka_guide topic=forge: fs_find/fs_read → fs_patch → project_test → project_commit → forge_ci → integration_ship → release", nil
}

type Deps struct {
	Catalog           func(string) map[string]toolforge.Tool
	RegisterComposite func(toolforge.Composite)
	Superseded        map[string]string
	Hidden            func(string) bool
	Readiness         func(string) string
	RequireAdmin      func(context.Context, string) error
	Call              func(context.Context, string, map[string]any) (map[string]any, error)
}

// findingsBudget keeps an audit inside the 128 KiB mutation output budget
// with room for the report: the 0.19.0 catalog in the sealed suite produced
// more findings than that and every audit failed "output budget exceeded".
const findingsBudget = 96 << 10

func boundFindings(all []toolforge.Finding) ([]toolforge.Finding, int) {
	size := 0
	for i, f := range all {
		raw, _ := json.Marshal(f)
		if size += len(raw) + 1; size > findingsBudget {
			return all[:i], len(all) - i
		}
	}
	return all, 0
}
