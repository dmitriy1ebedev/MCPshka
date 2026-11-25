package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/authn"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/toolstats"
	"os"
	"strings"
	"time"
)

// ownFaceIDGrant lists irreversible tools authorized by their own per-task
// Face ID grant, verified by the root broker against the pinned plan: the
// legacy local grant (mcpshka approval-grant in a root shell) on top of it
// sent the owner to a terminal after Face ID (26.10, LG IPK build).
var ownFaceIDGrant = map[string]bool{"approved_task_execute": true}

func addTool[In, Out any](r *toolRegistry, tool *mcp.Tool, handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)) {
	r.install(tooldef.New(tool, handler))
}

func (r *toolRegistry) install(d tooldef.Def) {
	tool := d.Tool
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	if note := supersededNote(tool.Name); note != "" && !strings.HasPrefix(tool.Description, note) {
		tool.Description = note + tool.Description
	}
	fillHints(tool)
	readOnly := tool.Annotations.ReadOnlyHint
	if r.catalog == nil {
		r.catalog = map[string]*mcp.Tool{}
	}
	r.catalog[tool.Name] = d.Catalog()
	r.tools[tool.Name] = readOnly
	if r.inputs == nil {
		r.inputs = map[string]func(map[string]any) (map[string]any, error){}
	}
	r.inputs[tool.Name] = func(input map[string]any) (map[string]any, error) {
		in, err := d.Decode(input)
		if err != nil {
			return nil, err
		}
		return object(in)
	}

	c, known := mutationContracts[tool.Name]
	if !known && !readOnly && strings.HasPrefix(tool.Name, "x_") {
		c, known = toolcontract.Replay(tool.Name), true
		if r.dynamicContracts == nil {
			r.dynamicContracts = map[string]mutationContract{}
		}
		r.dynamicContracts[tool.Name] = c
	}
	if !readOnly {
		if !known {
			panic("missing mutation contract: " + tool.Name)
		}
		if err := c.Validate(); err != nil {
			panic(err)
		}
		toolcontract.RequireInputKey(tool.Name, c, d.InputType)
	}
	if readOnly {
		r.readers[tool.Name] = func(ctx context.Context, input map[string]any) (any, error) {
			in, err := d.Decode(input)
			if err != nil {
				return nil, err
			}
			if err = authorizeTool(ctx, tool); err != nil {
				return nil, err
			}
			result, out, err := d.Handler(ctx, nil, in)
			if err == nil && result != nil && result.IsError {
				err = fmt.Errorf("%s probe reported an MCP error: %s", tool.Name, mcpErrorText(result))
			}
			return out, err
		}
	}
	if tool.OutputSchema == nil {
		tool.OutputSchema = d.DefaultOutput()
	}
	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
		zero := d.Zero
		ctx = invocationContext(ctx, req)
		if err := authorizeTool(ctx, tool); err != nil {
			return nil, zero, err
		}
		if readOnly {
			return d.Handler(ctx, req, in)
		}
		input, err := object(in)
		if err != nil {
			return nil, zero, err
		}
		result, value, err := r.mutate(ctx, tool, input, c,
			func() (*mcp.CallToolResult, any, error) {
				result, out, err := d.Handler(ctx, req, in)
				return result, out, err
			},
			func(key, operation, fingerprint string) (*mcp.CallToolResult, any, idempotency.State, error) {
				isError, text, out, state, err := d.ReadReceipt(r.store, key, operation, fingerprint)
				return &mcp.CallToolResult{IsError: isError, Content: receiptErrorContent(text)}, out, state, err
			})
		return result, value, err
	}
	wrapped = classifyHandler(wrapped)
	timed := wrapped
	wrapped = func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
		finishHTTPTool := toolstats.BeginHTTPTool(ctx)
		defer finishHTTPTool()
		start := time.Now()
		res, out, err := timed(ctx, req, in)
		toolstats.Default.Record(tool.Name, routeOf(ctx), start, time.Since(start), err != nil || res != nil && res.IsError)
		return res, out, err
	}
	if r.invokers == nil {
		r.invokers = map[string]toolInvoker{}
	}
	r.invokers[tool.Name] = toolInvoker(d.Invoker(wrapped))
	d.Install(r.server, wrapped)
	if r.catalogRevision != "" {
		r.catalogDirty = true
	}
}

func (r *toolRegistry) mutate(ctx context.Context, tool *mcp.Tool, input map[string]any, c mutationContract,
	handler func() (*mcp.CallToolResult, any, error),
	readReceipt func(string, string, string) (*mcp.CallToolResult, any, idempotency.State, error),
) (*mcp.CallToolResult, any, error) {
	// Runs before the admission gate: the target tool enters it itself.
	if isToolInvokeCompat(tool.Name, input) {
		snapshot, err := r.toolInvokeCompat(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, snapshot, nil
	}
	// A post-migration service user cannot install the privileged broker.
	// Reject this before allocating a mutation receipt or consuming a local
	// administrator approval token. Bootstrap is an independent root action.
	if tool.Name == "broker_install" && (brokerclient.Enabled() || os.Geteuid() != 0) {
		return nil, nil, errors.New("broker_install is unavailable after service-user activation; use the reviewed root-owned maintenance bootstrap")
	}
	if tool.Name == "exec_start" {
		if isCodexSessionCompat(tool.Name, input) {
			raw, _ := json.Marshal(input)
			var command ExecStartInput
			if json.Unmarshal(raw, &command) != nil {
				return nil, nil, errors.New("invalid Codex session compatibility input")
			}
			request, err := codexSessionCompatRequest(command)
			if err != nil {
				return nil, nil, err
			}
			name, readOnly := codexSessionTool(request.Action)
			if err := authorizeTool(ctx, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}); err != nil {
				return nil, nil, err
			}
		}
		if isArchiveCompat(tool.Name, input) {
			raw, _ := json.Marshal(input)
			var command ExecStartInput
			if json.Unmarshal(raw, &command) != nil {
				return nil, nil, errors.New("invalid archive compatibility request")
			}
			request, err := archiveCompatRequest(command)
			if err != nil {
				return nil, nil, err
			}
			if err := authorizeTool(ctx, &mcp.Tool{Name: "admin_archive_" + request.Action, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: request.Action == "status"}}); err != nil {
				return nil, nil, err
			}
		}
		command, _ := input["command"].(string)
		args, _ := input["args"].([]any)
		if (command == "mcpshka" || command == "/usr/local/bin/mcpshka") && len(args) > 0 && args[0] == "executor-fence" {
			if err := authorizeTool(ctx, &mcp.Tool{Name: "executor_fence", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}); err != nil {
				return nil, nil, err
			}
		}
	}
	if tool.Name == "exec_start" && isIntegrationMutation(tool.Name, input) {
		args, _ := input["args"].([]any)
		if len(args) < 2 {
			return nil, nil, errors.New("integration action missing")
		}
		name := "integration_recover"
		readOnly := false
		if args[0] == "integration-worker" {
			raw, _ := json.Marshal(input)
			var command ExecStartInput
			if json.Unmarshal(raw, &command) != nil {
				return nil, nil, errors.New("invalid integration worker request")
			}
			request, err := integrationWorkerCompatInput(command)
			if err != nil {
				return nil, nil, err
			}
			name, readOnly = integrationWorkerAction(request.Action)
		} else if args[0] != "integration-recover" {
			action, _ := args[1].(string)
			if action != "enqueue" && action != "cancel" && action != "status" {
				return nil, nil, errors.New("invalid integration action")
			}
			name = "integration_" + action
			readOnly = action == "status"
		}
		if err := authorizeTool(ctx, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}); err != nil {
			return nil, nil, err
		}
	}
	releaseAdmission, err := r.gate.enter(ctx, exclusiveYield(tool.Name, input))
	if err != nil {
		return nil, nil, err
	}
	defer releaseAdmission()
	key, err := toolcontract.RetryKey(c, input)
	if err != nil {
		return nil, nil, err
	}
	payload := make(map[string]any, len(input))
	for k, v := range input {
		if k != "contract_approval_token" && !(tool.Name == "root_task_authorize" && k == "code") {
			payload[k] = v
		}
	}
	if tool.Name == "root_task_authorize" || tool.Name == "root_task_execute" {
		payload["_root_task_owner"] = uploadOwner(ctx)
	}
	if tool.Name == "cloud_agent_task" || tool.Name == "cloud_agent_poll" || strings.HasPrefix(tool.Name, "admin_session_") || strings.HasPrefix(tool.Name, "admin_archive_") || strings.HasPrefix(tool.Name, "codex_session_") || isArchiveCompat(tool.Name, input) || isCodexSessionCompat(tool.Name, input) {
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, nil, err
		}
		payload["_admin_session_owner"] = owner
	}
	if exclusiveYield(tool.Name, input) {
		payload["_yield_owner"] = uploadOwner(ctx)
	}
	if isRecoveryCompat(tool.Name, input) {
		payload["_diagnostic_owner"] = uploadOwner(ctx)
	}
	if isArtifactUploadMutation(tool.Name, input) {
		payload["_artifact_owner"] = uploadOwner(ctx)
	}
	if isIntegrationMutation(tool.Name, input) {
		payload["_integration_owner"] = uploadOwner(ctx)
	}
	if toolcontract.SessionTool(tool.Name) || isDockerTerminal(tool.Name) {
		payload["_terminal_owner"] = uploadOwner(ctx)
	}
	key = mutationReceiptKey(ctx, tool.Name, key, input)
	fingerprint, err := idempotency.Fingerprint(tool.Name, payload)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256([]byte(key))
	ledgerKey := "contract:" + hex.EncodeToString(sum[:])
	operation := "contract/" + tool.Name
	if err := r.retirementPreflight(tool.Name, input, ledgerKey, operation, fingerprint); err != nil {
		return nil, nil, err
	}
	token, _ := input["contract_approval_token"].(string)
	cleanupFingerprint, _ := input["expected_fingerprint"].(string)
	irreversible := c.Irreversible && !(tool.Name == "project_cleanup_candidates" && cleanupFingerprint == "") && !ownFaceIDGrant[tool.Name]
	// A target that can never run is refused before any approval is asked
	// for (a grant would not help).
	if pre := toolPreflights[tool.Name]; pre != nil {
		if err := pre(ctx, input); err != nil {
			return nil, nil, err
		}
	}
	// Check missing authorization before creating a held receipt. The caller
	// can obtain a grant and submit this same untouched request once.
	if irreversible && token == "" {
		prior, e := r.store.Lookup(ledgerKey)
		if e != nil || prior.State != idempotency.Completed || prior.Operation != operation {
			return nil, nil, errors.New("APPROVAL_REQUIRED: create mutation_approval_request and obtain a local administrative grant")
		}
	}
	defer r.inflight.claim(ledgerKey)()
	begin, err := r.store.Begin(ledgerKey, operation, fingerprint)
	if err != nil {
		return nil, nil, err
	}
	replayPending := false
	if !begin.Fresh {
		_, _, state, err := readReceipt(ledgerKey, operation, fingerprint)
		if err != nil {
			return nil, nil, err
		}
		replayPending = state == idempotency.Pending && c.ReplayPending
		// The resource itself proves the earlier attempt left no effect: free
		// the key and run this same call afresh (MCP-135).
		if state == idempotency.Pending && !replayPending && r.provenNoEffect(tool.Name, ledgerKey, input) && r.store.Discard(ledgerKey, operation, fingerprint) == nil {
			r.releaseInner(tool.Name, input)
			if begin, err = r.store.Begin(ledgerKey, operation, fingerprint); err != nil {
				return nil, nil, err
			}
		}
		if !begin.Fresh && state != idempotency.Completed && !replayPending {
			return nil, nil, errors.New("contract outcome UNKNOWN; use mutation_probe; do not retry with a new key")
		}
	}
	if !begin.Fresh && !replayPending {
		previousResult, previous, _, err := readReceipt(ledgerKey, operation, fingerprint)
		if err != nil {
			return nil, nil, err
		}
		if tool.Name == "project_lease_acquire" || tool.Name == "project_lease_resume" {
			cached, e := object(previous)
			if e != nil {
				return nil, nil, e
			}
			path, _ := input["path"].(string)
			token, _ := cached["token"].(string)
			if r.leases == nil {
				return nil, nil, errors.New("local lease authority unavailable: HOLD")
			}
			if _, e = r.leases.ValidateHolder(path, token); e != nil {
				return nil, nil, e
			}
		}
		if tool.Name == "project_lease_admin_recover" {
			reader := r.lookupTool("project_lease_admin_recovery_status").reader
			if reader == nil {
				return nil, nil, errors.New("administrative recovery status authority unavailable: HOLD")
			}
			path, _ := input["path"].(string)
			current, e := reader(ctx, map[string]any{"path": path, "session_id": input["session_id"]})
			if e != nil {
				return nil, nil, e
			}
			if e := toolcontract.ValidateRecoveryReceipt(path, previous, current); e != nil {
				return nil, nil, e
			}
		}
		if isArtifactUploadMutation(tool.Name, input) {
			cached, e := object(previous)
			if e != nil {
				return nil, nil, e
			}
			if e = validateCachedUpload(ctx, r, tool.Name, input, cached); e != nil {
				return nil, nil, e
			}
		}
		return previousResult, previous, nil
	}
	phase := "UNKNOWN"
	var auditBefore, auditAfter contractEvidence
	if r.audit != nil {
		if err = r.audit.Log(audit.Event{Type: "mutation_contract", Result: "started", Details: map[string]any{"tool": tool.Name, "key_sha256": hex.EncodeToString(sum[:]), "fingerprint": fingerprint}}); err != nil {
			return nil, nil, r.store.RejectBeforeAudit(ledgerKey, begin, err)
		}
		defer func() {
			_ = r.audit.Log(audit.Event{Type: "mutation_contract", Result: phase, Details: map[string]any{"tool": tool.Name, "key_sha256": hex.EncodeToString(sum[:]), "before": auditBefore, "after": auditAfter}})
		}()
	}
	before, err := r.evidence(ctx, tool.Name, input, nil, true)
	auditBefore = before
	if err != nil {
		// Nothing ran yet: a fresh key is released, so the same call can be
		// repeated once the probe works, instead of an UNKNOWN receipt that
		// forbids both retry and a new key (MCP-132). A replayed pending key
		// keeps its hold — the earlier attempt may have had an effect.
		if begin.Fresh {
			if discardErr := r.store.Discard(ledgerKey, operation, fingerprint); discardErr == nil {
				phase = "rejected_no_effect"
				return nil, nil, fmt.Errorf("before probe failed; nothing was done, the same call may be repeated: %w", err)
			}
		}
		return nil, nil, fmt.Errorf("before probe failed; request held: %w", err)
	}
	// A request whose budget is already spent must not start the handler: it
	// would fail mid-way and leave an UNKNOWN receipt (MCP-135).
	if ctxErr := ctx.Err(); ctxErr != nil && begin.Fresh {
		if discardErr := r.store.Discard(ledgerKey, operation, fingerprint); discardErr == nil {
			phase = "rejected_no_effect"
			return nil, nil, failure.RejectedAs(failure.Deadline, "CONTEXT_DEADLINE", "mcpserver.tool_handler", "request budget was spent before the tool ran; nothing was done — repeat the same call", ctxErr)
		}
	}
	if irreversible {
		if _, err = r.approvals.ConsumeWithPlan(token, "mcp:"+tool.Name, []string{fingerprint}, "/root/repos/mcpshka", fingerprint); err != nil {
			return nil, nil, err
		}
	}

	result, out, err := handler()
	if err != nil {
		// A refusal proven to precede any effect releases the key: a
		// validation error left it PENDING forever, so the corrected retry
		// got "already used" or "outcome UNKNOWN" (BUG-20261103-5).
		if failure.IsNoEffect(err) {
			if !begin.Fresh {
				held := failure.NewHold(failure.FencedUnknown, "REPLAY_REFUSED_ORIGINAL_UNKNOWN", "mcpserver.mutate", "This retry was refused before execution; the original effect remains UNKNOWN. Reconcile the original receipt.", err)
				recoveryKey, _ := toolcontract.RetryKey(c, input)
				held.NextSafeAction = failure.Action{Tool: "gateway_reconcile", Arguments: map[string]any{"tool": tool.Name, "key": recoveryKey}}
				return result, out, held
			}
			if discardErr := r.store.Discard(ledgerKey, operation, fingerprint); discardErr == nil {
				phase = "rejected_no_effect"
			}
		}
		return result, out, err
	} // Otherwise remains PENDING even if a side effect preceded an error.
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > c.OutputLimit {
		return nil, nil, errors.New("mutation output budget exceeded; reconcile held request")
	}
	output, err := object(out)
	if err != nil {
		return nil, nil, err
	}
	after, err := r.evidence(ctx, tool.Name, input, output, false)
	auditAfter = after
	if err != nil {
		return result, out, fmt.Errorf("after probe failed; effect UNKNOWN: %w", err)
	}
	receipt := contractReceipt[any]{Output: out, Before: before, After: after}
	if result != nil && result.IsError {
		receipt.IsError = true
		receipt.ErrorText = mcpErrorText(result)
	}
	if err = r.store.Complete(ledgerKey, operation, fingerprint, receipt); err != nil {
		return result, out, fmt.Errorf("effect UNKNOWN: cannot persist receipt: %w", err)
	}
	phase = "completed"
	if result == nil {
		result = &mcp.CallToolResult{}
	}
	evidence, _ := json.Marshal(map[string]any{"contract": tool.Name, "before": before, "after": after})
	result.Content = append(result.Content, &mcp.TextContent{Text: string(evidence)})
	return result, out, nil
}

func authorizeTool(ctx context.Context, tool *mcp.Tool) error {
	identity, ok := authn.IdentityFromContext(ctx)
	if !ok {
		// In-process adapters/tests are trusted local callers. Network MCP always
		// passes through bearerAuth and therefore carries an authenticated identity.
		return nil
	}
	role := strings.ToLower(strings.TrimSpace(identity.Role))
	if role == "" {
		role = "operator"
	}
	readOnly := tool != nil && tool.Annotations != nil && tool.Annotations.ReadOnlyHint
	if role != "operator" && role != "reader" {
		return fmt.Errorf("identity %q has unsupported role %q", identity.ID, role)
	}
	if !readOnly && role != "operator" {
		return fmt.Errorf("identity %q role %q cannot call mutating tool %q", identity.ID, role, tool.Name)
	}
	allowed := false
	for _, scope := range identity.Scopes {
		if scope == "*" || scope == tool.Name {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("identity %q lacks scope for tool %q", identity.ID, tool.Name)
	}
	return nil
}

// toolPreflights check policy and independently approved agent scope before
// APPROVAL_REQUIRED or either approval/mutation receipt is recorded.
var toolPreflights = map[string]func(context.Context, map[string]any) error{}
