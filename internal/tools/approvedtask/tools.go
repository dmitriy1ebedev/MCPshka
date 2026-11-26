package approvedtask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/tooldef"
	"net"
	"net/http"
	"time"
)

type PrepareInput struct {
	rootbroker.ApprovedPlan
	IdempotencyKey string `json:"idempotency_key"`
	AdminSessionID string `json:"admin_session_id,omitempty" jsonschema:"live Face ID window: a plan off the control path (no network/power/kernel tools, no writes to system binaries, MCPshka, Lane, SSH, systemd) then runs without its own Face ID"`
}

type IDInput struct {
	ID             string `json:"id" jsonschema:"32-hex id from approved_task_prepare"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func Call(ctx context.Context, args ...string) (rootbroker.ApprovedResult, error) {
	var out rootbroker.ApprovedResult
	raw, err := brokerclient.Run(ctx, "approved-task", args...)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

// View is a task as the broker recorded it plus what the host
// itself observes now: the sandboxed run may lack the network (or die) after
// its effect happened, so the declared units and probe are checked here.
type View struct {
	rootbroker.ApprovedResult
	Verify []Fact `json:"verify,omitempty"`
	// Observed is declared_state_present when every check passes; it is
	// evidence, not proof the run made that state.
	Observed string `json:"observed,omitempty"`
}

type Fact struct {
	ID    string `json:"id"`
	State string `json:"state"` // PASS, FAIL
	Value string `json:"value"`
}

// Host-side checks, replaceable in tests.
var (
	approvedTransport = &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext}
	approvedDial      = func(ctx context.Context, hostport string) error {
		d := net.Dialer{Timeout: 3 * time.Second}
		c, err := d.DialContext(ctx, "tcp", hostport)
		if err == nil {
			c.Close()
		}
		return err
	}
	approvedHTTP = func(ctx context.Context, url string) (int, error) {
		ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		// Direct only: no proxy from the environment, no redirects, and one
		// connection per probe so repeated status polls cannot pile up sockets.
		c := http.Client{Transport: approvedTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	approvedUnitState = func(ctx context.Context, unit string) (string, error) {
		st, err := systemdsvc.NewFromEnvironment().Status(ctx, unit)
		return st.ActiveState, err
	}
)

// reachPreflight checks the plan's reach targets before the approval is
// spent; the first unreachable one is a HOLD, not a run.
func reachPreflight(ctx context.Context, p rootbroker.ApprovedPlan) error {
	for _, t := range p.Reach {
		if err := rootbroker.ValidateEndpoint(t); err != nil {
			return err
		}
		if err := approvedDial(ctx, t); err != nil {
			return fmt.Errorf("%s unreachable: %v", t, err)
		}
	}
	return nil
}

// hostVerify checks the declared units and probe of a task that ran.
func hostVerify(ctx context.Context, p rootbroker.ApprovedPlan) []Fact {
	var out []Fact
	for _, u := range p.Units {
		st, err := approvedUnitState(ctx, u)
		switch {
		case err != nil:
			out = append(out, Fact{"unit " + u, "FAIL", err.Error()})
		case st == "active":
			out = append(out, Fact{"unit " + u, "PASS", "active"})
		default:
			out = append(out, Fact{"unit " + u, "FAIL", "ActiveState=" + st})
		}
	}
	if p.Probe != "" {
		if rootbroker.ValidateProbe(p.Probe) != nil {
			out = append(out, Fact{"probe", "FAIL", "invalid probe"})
		} else if code, err := approvedHTTP(ctx, p.Probe); err != nil {
			out = append(out, Fact{"probe", "FAIL", err.Error()})
		} else if code/100 != 2 {
			out = append(out, Fact{"probe", "FAIL", fmt.Sprintf("HTTP %d", code)})
		} else {
			out = append(out, Fact{"probe", "PASS", fmt.Sprintf("HTTP %d", code)})
		}
	}
	return out
}

// view adds what the host observes of a task's declared units and
// probe. It never upgrades the effect: an active unit or a 200 may predate
// the run (Codex review 43526ef8), so a FAILED run stays unknown and the
// observation only tells the agent not to replay it blindly.
func view(ctx context.Context, t rootbroker.ApprovedResult) View {
	v := View{ApprovedResult: t}
	if t.Effect != "applied" && t.Effect != "unknown" {
		return v
	}
	if v.Verify = hostVerify(ctx, t.Plan); len(v.Verify) == 0 {
		return v
	}
	pass := true
	for _, f := range v.Verify {
		pass = pass && f.State == "PASS"
	}
	switch {
	case pass && t.Effect == "unknown":
		v.Observed = "declared_state_present"
		v.NextSafeAction = "do not execute again: effect stays UNKNOWN, but the host shows the declared units/probe healthy; confirm this run's result (version, content, unit file) before treating it as applied"
	case !pass && t.Effect == "applied":
		v.State = "VERIFY_FAILED"
		v.NextSafeAction = "the run finished but host-side verify FAILS: read service_status of the units before any further change"
	}
	return v
}

// Register installs approved_task_prepare, _execute and _status.
// Owner names the caller for the admin session check.
func Register(add func(tooldef.Def), owner func(context.Context) (string, error)) {
	add(tooldef.New(&mcp.Tool{Name: "approved_task_prepare", Meta: mcp.Meta{"ui": map[string]any{"resourceUri": ReviewURI}}, Description: "Plan one root command for the owner's Face ID: absolute root-owned executable (SHA-256 pinned at prepare), argv without a shell, workdir, writable paths, network yes/no, /dev devices, timeout 10..1800 s; optional units (services it installs: read-only service_status afterwards), probe (http://<private-ip>:<port>/path checked from the host after the run) and reach (ip:port that must answer before the approval is spent). With admin_session_id a plan off the control path is covered by the live Face ID window and starts at once (covered_by_session); otherwise returns approval_url: give it to the owner (the page shows the generated command line). The server continues newly prepared tasks automatically after Face ID, once, in the same sandbox. Poll approved_task_status; no chat acknowledgement. Same key and plan replay the original task; expiry never renews new tasks automatically. A different plan under that key is HOLD. Legacy manual tasks retain their original contract.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in PrepareInput) (*mcp.CallToolResult, rootbroker.ApprovedResult, error) {
		if in.IdempotencyKey == "" {
			return nil, rootbroker.ApprovedResult{}, errors.New("idempotency_key required")
		}
		sum := sha256.Sum256([]byte("approved-task\x00" + in.IdempotencyKey))
		resolved, err := ResolveArtifacts(in.ApprovedPlan)
		if err != nil {
			return nil, rootbroker.ApprovedResult{}, err
		}
		plan, _ := json.Marshal(resolved)
		id := hex.EncodeToString(sum[:16])
		out, err := Call(ctx, "prepare", id, string(plan))
		if err != nil || in.AdminSessionID == "" || out.State != "AWAITING_FACE_ID" || out.ApprovalState == "authorized" {
			return nil, out, err
		}
		who, err := owner(ctx)
		if err != nil {
			return nil, out, err
		}
		covered, err := Call(ctx, "authorize-session", id, in.AdminSessionID, who)
		if err != nil {
			// Out of session scope: the per-plan Face ID link still works.
			out.NextSafeAction = err.Error()
			return nil, out, nil
		}
		if next, cerr := continueApprovedTask(ctx, id); cerr == nil {
			covered = next
		}
		return nil, covered, nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "approved_task_execute", Description: "Run a prepared approved_task once, after the owner confirmed it with Face ID; refuses if the executable or plan changed. Returns RUNNING; poll approved_task_status for exit code and output.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in IDInput) (*mcp.CallToolResult, rootbroker.ApprovedResult, error) {
		cur, err := Call(ctx, "status", in.ID)
		if err != nil {
			return nil, cur, err
		}
		if cur.State == "AWAITING_FACE_ID" {
			if err := reachPreflight(ctx, cur.Plan); err != nil {
				return nil, cur, fmt.Errorf("HOLD preflight: %v; the approval was NOT spent: make the target reachable (e.g. turn the TV on), then call approved_task_execute again with the same id; if approval_state lapses, renew with approved_task_prepare and the same idempotency_key", err)
			}
		}
		out, err := Call(ctx, "execute", in.ID)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "approved_task_status", Description: "State of an approved_task: AWAITING_FACE_ID (approval_state awaiting_face_id, authorized, approval_expired, authorization_expired), RUNNING, DONE or FAILED with exit code, bounded output, effect (applied, none = nothing ran, unknown), verify[] = host observations of the declared units/probe (observed declared_state_present) and next_safe_action. Never re-execute an unknown effect. Read-only.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in IDInput) (*mcp.CallToolResult, View, error) {
		out, err := Call(ctx, "status", in.ID)
		if err != nil {
			return nil, View{ApprovedResult: out}, err
		}
		return nil, view(ctx, out), nil
	}))
}
