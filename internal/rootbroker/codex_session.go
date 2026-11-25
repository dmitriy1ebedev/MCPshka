package rootbroker

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"mcpshka/internal/codexcoord"
	"mcpshka/internal/heavyqueue"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

//go:embed codex_session_client.py
var codexSessionClient string

const codexSessionSchema = "mcpshka.codex-session/v1"

var codexThreadID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type codexSessionIntent struct {
	Schema         string `json:"schema"`
	ID             string `json:"id"`
	AdminSessionID string `json:"admin_session_id"`
	Owner          string `json:"owner"`
	Generation     int64  `json:"generation"`
	CreationKey    string `json:"creation_key"`
	LoadKey        string `json:"load_key"`
	Action         string `json:"action"`
	ThreadID       string `json:"thread_id"`
	At             string `json:"at"`
}

type codexSessionEvent struct {
	Diagnostic     *codexcoord.Diagnostic `json:"diagnostic,omitempty"`
	Schema         string                 `json:"schema"`
	ID             string                 `json:"id"`
	State          string                 `json:"state"`
	ThreadID       string                 `json:"thread_id"`
	SessionID      string                 `json:"codex_session_id"`
	PolicyVerified bool                   `json:"policy_verified"`
	Isolated       bool                   `json:"isolated"`
	Servers        []string               `json:"disabled_servers"`
	ObservedAt     string                 `json:"observed_at"`
}

type CodexSessionResult struct {
	Diagnostic     *codexcoord.Diagnostic `json:"diagnostic,omitempty"`
	ID             string                 `json:"id"`
	State          string                 `json:"state"`
	ThreadID       string                 `json:"thread_id,omitempty"`
	SessionID      string                 `json:"codex_session_id,omitempty"`
	LastObservedAt string                 `json:"last_observed_at,omitempty"`
	Mode           string                 `json:"mode"`
	ReasonCode     string                 `json:"reason_code,omitempty"`
	NextAction     string                 `json:"next_action"`
	Unit           string                 `json:"unit"`
	RetryAllowed   bool                   `json:"retry_allowed"`
}

func codexSessionID(session, owner, creation, load string) string {
	raw, _ := json.Marshal([]string{session, owner, creation, load})
	return shaHex(raw)[:32]
}
func codexSessionPath(root, id, phase string) string {
	return filepath.Join(root, "codex-session-"+id+"-"+phase+".json")
}
func codexSessionUnit(id string) string { return "mcpshka-codex-session-" + id + ".service" }

func readCodexSessionIntent(root, id string) (codexSessionIntent, error) {
	var p codexSessionIntent
	if err := archiveDirectory(root, uint32(os.Geteuid())); err != nil {
		return p, err
	}
	if !validHex(id, 32) {
		return p, errors.New("invalid Codex operation identity")
	}
	raw, err := readAdminRecord(codexSessionPath(root, id, "intent"), 8192)
	if err != nil {
		return p, err
	}
	if err = decodeUniqueHistorical(raw, &p, 10); err != nil {
		return p, err
	}
	at, e := time.Parse(time.RFC3339Nano, p.At)
	if p.Schema != codexSessionSchema || p.ID != id || !validHex(p.AdminSessionID, 32) || !safeOwner(p.Owner) || p.Owner == "local-stdio" || p.Generation <= 0 || p.Generation >= 1<<53 || !validHex(p.CreationKey, 32) || e != nil || at.IsZero() || codexSessionID(p.AdminSessionID, p.Owner, p.CreationKey, p.LoadKey) != id || (p.Action != "create" && p.Action != "load") || (p.Action == "create" && (p.LoadKey != "" || p.ThreadID != "")) || (p.Action == "load" && (!validHex(p.LoadKey, 32) || !codexThreadID.MatchString(p.ThreadID))) {
		return p, errors.New("Codex intent binding changed: HOLD")
	}
	return p, nil
}

func readCodexSessionEvent(root string, p codexSessionIntent) (codexSessionEvent, error) {
	latest := codexSessionEvent{Schema: codexSessionSchema, ID: p.ID, State: "DISPATCHED", ThreadID: p.ThreadID}
	if err := archiveDirectory(root, uint32(os.Geteuid())); err != nil {
		return latest, err
	}
	claimed, identified := false, false
	for _, phase := range []string{"CLAIMED", "IDENTIFIED", "HELD", "READY"} {
		raw, err := readAdminRecord(codexSessionPath(root, p.ID, phase), 8192)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return latest, err
		}
		var r codexSessionEvent
		if err = decodeUniqueHistorical(raw, &r, codexcoord.SessionEventFields(raw)); err != nil {
			return latest, err
		}
		at, e := time.Parse(time.RFC3339Nano, r.ObservedAt)
		if !codexcoord.ValidDiagnostic(r.Diagnostic) || r.Schema != codexSessionSchema || r.ID != p.ID || r.State != phase || e != nil || at.IsZero() || len(r.Servers) > 128 || (r.ThreadID != "" && !codexThreadID.MatchString(r.ThreadID)) || (r.SessionID != "" && !codexThreadID.MatchString(r.SessionID)) || (p.ThreadID != "" && r.ThreadID != p.ThreadID) || (phase != "CLAIMED" && !claimed) || ((phase == "IDENTIFIED" || phase == "READY") && r.ThreadID == "") || (phase == "READY" && (!identified || !r.PolicyVerified || !r.Isolated || r.SessionID == "" || latest.State == "HELD")) || (latest.ThreadID != "" && latest.ThreadID != r.ThreadID) {
			return latest, errors.New("Codex phase evidence changed: HOLD")
		}
		claimed = claimed || phase == "CLAIMED"
		identified = identified || phase == "IDENTIFIED"
		latest = r
	}
	return latest, nil
}

func codexSessionStatus(ctx context.Context, root string, p codexSessionIntent) (CodexSessionResult, error) {
	r, err := readCodexSessionEvent(root, p)
	out := CodexSessionResult{Diagnostic: r.Diagnostic, ID: p.ID, State: r.State, ThreadID: r.ThreadID, SessionID: r.SessionID, LastObservedAt: r.ObservedAt, Mode: "read-only", Unit: codexSessionUnit(p.ID), NextAction: "inspect_same_intent_or_load_known_thread_never_recreate"}
	if r.State == "READY" {
		out.NextAction = "check_live_session_before_task_submit"
	} else {
		out.State = "HOLD"
		out.ReasonCode = "SESSION_READINESS_UNCONFIRMED"
		if r.ThreadID == "" {
			out.State = "UNKNOWN"
			out.ReasonCode = "CREATION_REPLY_UNCONFIRMED"
		}
	}
	return out, err
}

// Public READY waits for the independent worker to release native admission.
func publicCodexSessionStatus(ctx context.Context, p codexSessionIntent) (CodexSessionResult, error) {
	out, err := codexSessionStatus(ctx, taskRoot, p)
	if err != nil {
		return out, err
	}
	alive, err := administrativeWorkerAlive(ctx, out.Unit)
	if err != nil {
		return out, err
	}
	if alive {
		out.State = "RUNNING"
		out.ReasonCode = "WORKER_ACTIVE"
		out.NextAction = "wait_same_worker_before_task_submit"
	}
	return out, nil
}

func runAdminCodexSession(ctx context.Context, args []string) (CodexSessionResult, error) {
	var zero CodexSessionResult
	if len(args) != 5 || (args[0] != "codex-create" && args[0] != "codex-load" && args[0] != "codex-status") || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" || !validHex(args[3], 32) || (args[4] != "" && !validHex(args[4], 32)) || (args[0] == "codex-create" && args[4] != "") || (args[0] == "codex-load" && args[4] == "") {
		return zero, errors.New("owner-bound Codex intent keys required")
	}
	id := codexSessionID(args[1], args[2], args[3], args[4])
	p, err := readCodexSessionIntent(taskRoot, id)
	if args[0] == "codex-status" {
		if os.IsNotExist(err) {
			return CodexSessionResult{ID: id, State: "NOT_PREPARED", Mode: "read-only", NextAction: "create_or_load_owned_session", Unit: codexSessionUnit(id)}, nil
		}
		if err != nil {
			return zero, err
		}
		return publicCodexSessionStatus(ctx, p)
	}
	if err == nil {
		return publicCodexSessionStatus(ctx, p)
	}
	if !os.IsNotExist(err) {
		return zero, err
	}
	lock, err := openAdminSessionLock(taskRoot)
	if err != nil {
		return zero, err
	}
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
	current, errGrant := readAdminSession(taskRoot, time.Now())
	if errGrant != nil {
		return zero, errGrant
	}
	if errGrant = requireAdminSession(current, args[1], args[2], current.Generation, time.Now()); errGrant != nil {
		return zero, errGrant
	}
	p = codexSessionIntent{Schema: codexSessionSchema, ID: id, AdminSessionID: args[1], Owner: args[2], Generation: current.Generation, CreationKey: args[3], LoadKey: args[4], Action: "create", At: time.Now().UTC().Format(time.RFC3339Nano)}
	if args[0] == "codex-load" {
		p.Action = "load"
		creation, err := readCodexSessionIntent(taskRoot, codexSessionID(args[1], args[2], args[3], ""))
		if err != nil {
			return zero, err
		}
		event, err := readCodexSessionEvent(taskRoot, creation)
		if err != nil {
			return zero, err
		}
		if !codexThreadID.MatchString(event.ThreadID) {
			return zero, errors.New("creation outcome UNKNOWN; never recreate")
		}
		p.ThreadID = event.ThreadID
	}
	if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-broker"); err != nil {
		return zero, err
	}
	// The immutable intent is the dispatch barrier, including a lost launch reply.
	if err := writeRootOnly(codexSessionPath(taskRoot, id, "intent"), p, true); err != nil {
		return zero, err
	}
	lock.Close()
	lock = nil
	command := codexSessionServiceArgs(id)
	if err := exec.CommandContext(ctx, "/usr/bin/systemd-run", command...).Run(); err != nil {
		out, e := publicCodexSessionStatus(ctx, p)
		return out, e
	}
	return publicCodexSessionStatus(ctx, p)
}

func codexSessionServiceArgs(id string) []string {
	return heavyqueue.HeavyUnitArgs([]string{"--unit=" + codexSessionUnit(id), "--collect", "--quiet", "--no-block",
		"--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077",
		"--property=RuntimeMaxSec=40s", "--property=MemoryMax=128M", "--property=CPUQuota=50%", "--property=TasksMax=16",
		"--property=NoNewPrivileges=yes", "--property=PrivateDevices=yes", "--property=PrivateNetwork=yes",
		// The daemon socket resolves into host /tmp; PrivateTmp would hide it.
		"--property=PrivateTmp=no", "--property=ProtectHome=read-only", "--property=ProtectSystem=strict",
		// Peer /proc/exe verification needs ptrace-read against the privileged daemon.
		"--property=ReadWritePaths=" + taskRoot, "--property=CapabilityBoundingSet=CAP_SYS_PTRACE", "--property=RestrictAddressFamilies=AF_UNIX",
		"--property=RestrictNamespaces=yes", "--property=ProtectKernelTunables=yes", "--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes",
		"/usr/local/libexec/mcpshka-broker", "codex-session-worker", id})
}

type codexClientResult struct {
	ThreadID       string   `json:"thread_id"`
	SessionID      string   `json:"codex_session_id"`
	PolicyVerified bool     `json:"policy_verified"`
	Isolated       bool     `json:"isolated"`
	Servers        []string `json:"disabled_servers"`
}

func callCodexSessionClient(ctx context.Context, input any) (codexClientResult, error) {
	var out codexClientResult
	raw, err := json.Marshal(input)
	if err != nil {
		return out, err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-c", codexSessionClient)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(raw)
	var output limitedBuffer
	output.limit = 16384
	cmd.Stdout = &output
	if cmd.Run() != nil || output.truncated {
		return out, errors.New("Codex daemon result unconfirmed")
	}
	if err = json.Unmarshal(output.Bytes(), &out); err != nil {
		return out, err
	}
	return out, nil
}

func RunCodexSessionWorker(ctx context.Context, args []string) (CodexSessionResult, error) {
	var zero CodexSessionResult
	if os.Geteuid() != 0 || len(args) != 1 || !validHex(args[0], 32) {
		return zero, errors.New("fixed root Codex worker required")
	}
	lock, err := openAdminSessionLock(taskRoot)
	if err != nil {
		return zero, err
	}
	defer lock.Close()
	admission, err := openMutationLock(filepath.Join(taskRoot, "codex-admission.lock"))
	if err != nil {
		return zero, err
	}
	defer admission.Close()
	p, err := readCodexSessionIntent(taskRoot, args[0])
	if err != nil {
		return zero, err
	}
	return executeCodexSessionIntent(ctx, taskRoot, p, callCodexSessionClient)
}

func executeCodexSessionIntent(ctx context.Context, root string, p codexSessionIntent, client func(context.Context, any) (codexClientResult, error)) (CodexSessionResult, error) {
	r, err := readCodexSessionEvent(root, p)
	if err != nil {
		return CodexSessionResult{}, err
	}
	if r.State != "DISPATCHED" {
		return codexSessionStatus(ctx, root, p)
	}
	current, err := readAdminSession(root, time.Now())
	if err != nil {
		return CodexSessionResult{}, err
	}
	if err = requireAdminSession(current, p.AdminSessionID, p.Owner, p.Generation, time.Now()); err != nil {
		return CodexSessionResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return CodexSessionResult{}, err
	}
	r.State = "CLAIMED"
	r.Diagnostic = codexcoord.SessionDiagnostic(p.Action, false, false, false)
	r.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = writeRootOnly(codexSessionPath(root, p.ID, r.State), r, true); err != nil {
		return CodexSessionResult{}, err
	}
	input := map[string]any{"action": p.Action}
	if p.Action == "load" {
		input["thread_id"] = p.ThreadID
	}
	identified := false
	data, e := client(ctx, input)
	if e == nil && codexThreadID.MatchString(data.ThreadID) && (p.ThreadID == "" || p.ThreadID == data.ThreadID) {
		r.State = "IDENTIFIED"
		identified = true
		r.Diagnostic = codexcoord.SessionDiagnostic(p.Action, true, data.PolicyVerified, false)
		r.ThreadID = data.ThreadID
		r.SessionID = data.SessionID
		r.PolicyVerified = data.PolicyVerified
		r.Servers = data.Servers
		r.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err = writeRootOnly(codexSessionPath(root, p.ID, r.State), r, true); err != nil {
			return CodexSessionResult{}, err
		}
		if !r.PolicyVerified {
			e = errors.New("read-only policy not confirmed")
		} else {
			proof, probeErr := client(ctx, map[string]any{"action": "probe", "thread_id": r.ThreadID, "servers": r.Servers})
			e = probeErr
			if e == nil && (proof.ThreadID != r.ThreadID || !proof.Isolated) {
				e = errors.New("thread isolation not confirmed")
			}
		}
	} else if e == nil {
		e = errors.New("daemon identity unconfirmed")
	}
	r.State = "HELD"
	r.Isolated = false
	if e == nil {
		r.State = "READY"
		r.Isolated = true
	}
	r.Diagnostic = codexcoord.SessionDiagnostic(p.Action, identified, r.PolicyVerified, r.Isolated)
	r.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = writeRootOnly(codexSessionPath(root, p.ID, r.State), r, true); err != nil {
		return CodexSessionResult{}, err
	}
	return codexSessionStatus(ctx, root, p)
}
