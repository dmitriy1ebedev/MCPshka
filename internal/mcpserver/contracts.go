package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/toolcontract"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/approval"
	"mcpshka/internal/audit"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/readiness"
)

type mutationContract = toolcontract.Contract

var mutationContracts = toolcontract.Contracts

type probeHandler func(context.Context, map[string]any) (any, error)
type toolRegistry struct {
	catalogMu        sync.RWMutex
	compositeMu      sync.Mutex
	dynamicContracts map[string]mutationContract
	shipRuns         sync.Map
	inflight         keyClaims
	catalog          map[string]*mcp.Tool
	catalogRevision  string
	// catalogDirty defers the catalog hash to the next snapshot: refreshing it
	// on every late install made New() quadratic (0.18.0 sealed test 600 s).
	catalogDirty   bool
	policyRevision string
	gate           mutationGate
	yieldRecovery  func(context.Context, string, string, map[string]any) (WorkYieldResult, error)
	server         *mcp.Server
	audit          *audit.Logger
	store          *idempotency.Store
	approvals      *approval.Store
	leases         *projectlease.Service
	readers        map[string]probeHandler
	tools          map[string]bool
	inputs         map[string]func(map[string]any) (map[string]any, error)
	// invokers hold each tool's fully wrapped handler for tool_invoke.
	invokers map[string]toolInvoker
	// receipts stores exec_start tool-invoke compatibility results as jobs.
	receipts *jobs.Manager
	// clients records each MCP client's last tools/list.
	clients *clientCatalog
	// health feeds capability readiness; readiness caches self-tests.
	health    *healthlayer.Collector
	readiness readiness.Cache
}

func (r *toolRegistry) validate() {
	view := r.registryView()
	for name, readOnly := range view.tools {
		if readOnly {
			// Status probes such as self_heal_status have no mutation/replay contract.
			continue
		}
		c, ok := view.contracts[name]
		if !ok {
			panic("missing mutation contract: " + name)
		}
		if err := c.Validate(); err != nil {
			panic(err)
		}
		if r.lookupTool(c.Probe).reader == nil {
			panic("missing read-only probe for " + name)
		}
		if !c.Irreversible && c.Recovery != "bounded_expiry" {
			if _, ok := view.tools[c.Recovery]; !ok {
				panic("missing compensation tool for " + name)
			}
		}
	}
	if gaps := r.allProbeGaps(); len(gaps) > 0 {
		panic("mutation probes cannot be called: " + strings.Join(gaps, "; "))
	}
}
func object(v any) (map[string]any, error) { return toolcontract.Object(v) }

type contractEvidence = toolcontract.Evidence
type contractReceipt[T any] = toolcontract.Receipt[T]

func (r *toolRegistry) evidence(ctx context.Context, name string, in, out map[string]any, before bool) (contractEvidence, error) {
	c, _ := r.contractFor(name)
	probe := c.Probe
	if name == "exec_start" {
		raw, err := json.Marshal(in)
		var command ExecStartInput
		if err != nil || json.Unmarshal(raw, &command) != nil {
			return contractEvidence{}, errors.New("invalid compatibility probe input")
		}
		if isArchiveCompat(name, in) {
			request, err := archiveCompatRequest(command)
			if err != nil {
				return contractEvidence{}, err
			}
			probe = "admin_archive_status"
			in = map[string]any{"session_id": request.SessionID, "scope": request.Scope}
		}
		if isCodexSessionCompat(name, in) {
			request, err := codexSessionCompatRequest(command)
			if err != nil {
				return contractEvidence{}, err
			}
			probe = "codex_session_creation_status"
			in = map[string]any{"admin_session_id": request.AdminSessionID, "creation_key": request.CreationKey, "load_key": request.LoadKey}
		}
		if (command.Command == "mcpshka" || command.Command == "/usr/local/bin/mcpshka") && len(command.Args) > 0 && command.Args[0] == "executor-canary" {
			_, request, err := executorCanaryRequest(command)
			if err != nil {
				return contractEvidence{}, err
			}
			probe = "executor_runtime_status"
			in = map[string]any{"permit_id": request.PermitID, "launch_id": request.LaunchID, "workdir": request.Workdir}
		}
		if (command.Command == "mcpshka" || command.Command == "/usr/local/bin/mcpshka") && len(command.Args) > 0 && command.Args[0] == "integration-worker" {
			request, err := integrationWorkerCompatInput(command)
			if err != nil {
				return contractEvidence{}, err
			}
			probe = "integration_worker_status"
			if request.Action == "launch" {
				probe = "integration_approval_status"
			}
			if request.Action == "abort" {
				probe = "integration_recovery_status"
			}
			in = map[string]any{"permit_id": request.PermitID, "approval_id": request.ApprovalID, "workdir": request.Workdir}
		}
		if (command.Command == "mcpshka" || command.Command == "/usr/local/bin/mcpshka") && len(command.Args) > 0 && command.Args[0] == "integration-recover" {
			request, err := integrationRecoveryCompatInput(command)
			if err != nil {
				return contractEvidence{}, err
			}
			// A transport receipt is not the integration outcome. The same
			// fixed root claim is observed before, after and on cold probes.
			probe = "integration_recovery_status"
			in = map[string]any{"permit_id": request.PermitID, "approval_id": request.ApprovalID, "workdir": request.Workdir}
		}
	}
	probe, args, err := toolcontract.ProbeArguments(name, probe, in, out, before)
	if err != nil {
		return contractEvidence{}, err
	}
	args = r.fillProbeArgs(probe, args, in)
	return toolcontract.Observe(ctx, probe, args, r.lookupTool(probe).reader)
}

type MutationApprovalInput struct {
	Tool           string         `json:"tool"`
	Input          map[string]any `json:"input"`
	IdempotencyKey string         `json:"idempotency_key"`
}
type MutationProbeInput = toolcontract.MutationProbeInput

func registerContractTools(r *toolRegistry) {
	closed := false
	addTool(r, &mcp.Tool{Name: "mutation_contracts", Description: "Compact mandatory mutation contracts: retry key, live probe, compensation or approval-gated irreversibility, output cap and before/after evidence. Job cancellation stops future execution; it cannot undo arbitrary completed command side effects.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, map[string]mutationContract, error) {
		return nil, r.snapshotRegistry().contracts, nil
	})
	addTool(r, &mcp.Tool{Name: "mutation_probe", Description: "Reconcile a durable mutation receipt without replaying its effect. PENDING remains UNKNOWN; never clears it or authorizes a new key. Optional exact input adds a bounded live resource observation. No result payloads or secrets are returned.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in MutationProbeInput) (*mcp.CallToolResult, map[string]any, error) {
		out, err := r.probeMutation(ctx, in)
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "mutation_approval_request", Description: "Request, never grant, a one-time administrative approval bound to an exact irreversible tool payload. Use approval_status; a trusted local administrator grants through the existing approval CLI.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in MutationApprovalInput) (*mcp.CallToolResult, approval.Request, error) {
		c, ok := r.contractFor(in.Tool)
		if !ok || !c.Irreversible {
			return nil, approval.Request{}, errors.New("tool is not approval-gated irreversible")
		}
		normalize := r.lookupTool(in.Tool).normalize
		if normalize == nil {
			return nil, approval.Request{}, errors.New("target tool not registered")
		}
		normalized, err := normalize(in.Input)
		if err != nil {
			return nil, approval.Request{}, err
		}
		if pre := toolPreflights[in.Tool]; pre != nil {
			if err := pre(ctx, normalized); err != nil {
				return nil, approval.Request{}, err
			}
		}
		terminal := isDockerTerminal(in.Tool)
		owner := ""
		if terminal {
			owner = uploadOwner(ctx)
		}
		out, err := toolcontract.RequestMutationApproval(r.approvals, in.Tool, normalized, terminal, owner)
		return nil, out, err
	})
}

func isInteractiveLeaseMutation(name string) bool {
	switch name {
	case "project_lease_acquire", "project_lease_resume", "project_lease_release", "project_lease_heartbeat":
		return true
	}
	return false
}

func mutationReceiptKey(ctx context.Context, name, key string, input map[string]any) string {
	if isInteractiveLeaseMutation(name) {
		return yieldHash(uploadOwner(ctx) + "\x00" + name + "\x00" + key)
	}
	if strings.HasPrefix(name, "storage_") || strings.HasPrefix(name, "backup_") || name == "knowledge_import_chat" {
		return yieldHash(uploadOwner(ctx) + "\x00" + name + "\x00" + key)
	}
	if toolcontract.SessionTool(name) || isDockerTerminal(name) {
		return yieldHash(uploadOwner(ctx) + "\x00" + name + "\x00" + key)
	}
	if exclusiveYield(name, input) || isRecoveryCompat(name, input) || isIntegrationMutation(name, input) {
		return yieldHash(uploadOwner(ctx) + "\x00" + name + "\x00" + key)
	}
	return uploadReceiptKey(ctx, name, key, input)
}

func (r *toolRegistry) probeMutation(ctx context.Context, in MutationProbeInput) (map[string]any, error) {
	opts := toolcontract.ProbeOptions{
		ReceiptKey:               mutationReceiptKey(ctx, in.Tool, in.Key, in.Input),
		PrincipalScoped:          toolcontract.SessionTool(in.Tool) || isInteractiveLeaseMutation(in.Tool) || exclusiveYield(in.Tool, in.Input) || isRecoveryCompat(in.Tool, in.Input) || isIntegrationMutation(in.Tool, in.Input),
		RetirementRejectedPrefix: retirementRejectedBeforeWorker,
		HandlerUnknown: func(err error) bool {
			var class *classifiedError
			return errors.As(classifyFailure(err), &class) && class.Class == FailFencedUnknown
		},
		Observe: func(ctx context.Context, out map[string]any) (contractEvidence, error) {
			return r.evidence(ctx, in.Tool, in.Input, out, false)
		},
	}
	if in.Tool == "project_lease_acquire" {
		opts.HandlerKey = mutationReceiptKey(ctx, in.Tool, in.Key, in.Input)
		opts.HandlerOperation = leaseAcquireOperation
	}
	if in.Tool == "forge_ci" {
		// Forge handler receipts are scoped to the authenticated owner, unlike legacy outer receipts.
		opts.HandlerKey = uploadOwner(ctx) + "\x00" + in.Key
	}
	opts.DeploymentOutput = func() (map[string]any, bool) {
		if inner, e := r.store.Lookup(in.Key); e == nil && inner.Operation == reservedDeployOperation {
			if id, e := reservedDeploymentID(in.Key, inner); e == nil {
				return map[string]any{"deployment_id": id}, true
			}
		}
		return nil, false
	}
	opts.YieldProof = func(ctx context.Context, fingerprint string) (map[string]any, bool) {
		if !exclusiveYield(in.Tool, in.Input) || len(in.Input) == 0 || r.yieldRecovery == nil {
			return nil, false
		}
		if normalize := r.lookupTool(in.Tool).normalize; normalize != nil {
			if exact, e := normalize(in.Input); e == nil {
				delete(exact, "contract_approval_token")
				exact["_yield_owner"] = uploadOwner(ctx)
				if fp, e := idempotency.Fingerprint(in.Tool, exact); e == nil && fp == fingerprint {
					if proof, e := r.yieldRecovery(ctx, in.Tool, in.Key, in.Input); e == nil {
						out, _ := object(proof)
						return out, true
					}
				}
			}
		}
		return nil, false
	}
	if c, ok := r.contractFor(in.Tool); ok {
		opts.Contract = &c
	}
	return toolcontract.ProbeMutation(ctx, r.store, in, opts)
}
