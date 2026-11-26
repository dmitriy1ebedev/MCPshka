package networktools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/audit"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/networkops"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"strings"
)

func Register(add func(tooldef.Def), d Deps) {
	manager := d.Manager
	idempotencyStore := d.Store
	networkService := d.Network
	controlService := d.Control
	laneService := d.Lane
	closed := false
	destructive := true
	nonDestructive := false

	add(tooldef.New(&mcp.Tool{
		Name:        "network_stage_status",
		Description: "Return compact MCPshka control-path state plus LANE canary/timed-rollback state without mutating the network.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, NetworkStageStatusResult, error) {
		control, err := controlService.Snapshot(ctx)
		if err != nil {
			return nil, NetworkStageStatusResult{}, err
		}
		// Status is read-only: report independent observations even if LANE
		// cannot be reached. Mutating LANE calls still fail closed separately.
		lane, laneErr := laneService.Status(ctx)
		snapshot, networkErr := networkService.Snapshot(ctx)
		var networkDigest networkops.StateDigest
		if networkErr == nil {
			networkDigest = networkops.Digest(snapshot)
		}
		return nil, toolcontract.MakeNetworkStageStatus(control, lane, laneErr, networkDigest, networkErr), nil
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "network_stage_apply",
		Description: "Apply a staged persistent network mutation only through authoritative LANE. Requires explicit affected management paths and idempotency. LANE must return PENDING_COMMIT with an armed 30..900 second rollback timer or MCPshka immediately requests rollback.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in NetworkStageApplyInput) (*mcp.CallToolResult, NetworkStageApplyResult, error) {
		kind := strings.ToLower(strings.TrimSpace(in.Kind))
		if kind == "" {
			kind = "network"
		}
		switch kind {
		case "network", "vpn", "dns", "routes", "firewall":
		default:
			return nil, NetworkStageApplyResult{}, fmt.Errorf("unsupported network mutation kind %q", kind)
		}
		if len(in.AffectedPaths) == 0 {
			return nil, NetworkStageApplyResult{}, errors.New("affected_paths is required for staged network mutations")
		}
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, NetworkStageApplyResult{}, errors.New("idempotency_key is required for network_stage_apply")
		}

		preflight, err := controlService.Preflight(ctx, controlplane.MutationRequest{
			Kind: kind, ViaProvider: "lane", AffectedPaths: in.AffectedPaths,
			Staged: true, RollbackSeconds: laneprovider.ExpectedRollback,
		})
		out := NetworkStageApplyResult{Preflight: preflight}
		if err != nil {
			return nil, out, err
		}
		if !preflight.Allowed {
			return nil, out, nil
		}
		preview, err := laneService.Preview(ctx, in.Client)
		out.Preview = preview
		if err != nil {
			return nil, out, err
		}
		payload := struct {
			Kind          string
			Client        string
			AffectedPaths []string
		}{kind, strings.TrimSpace(in.Client), append([]string(nil), in.AffectedPaths...)}
		out, err = idempotency.Run(idempotencyStore, in.IdempotencyKey, "network_stage_apply", payload, func() (NetworkStageApplyResult, error) {
			result := NetworkStageApplyResult{Preflight: preflight, Preview: preview}
			before, err := networkService.Snapshot(ctx)
			if err != nil {
				return result, fmt.Errorf("capture network state before LANE apply: %w", err)
			}
			result.Before = networkops.Digest(before)
			plan, err := laneService.CreatePlan(ctx, in.Client)
			result.Plan = plan
			if err != nil {
				return result, err
			}
			stage, err := laneService.Apply(ctx, plan.Plan.PlanID)
			result.Stage = stage
			if err != nil {
				return result, err
			}
			if stage.Status != "PENDING_COMMIT" || !stage.RollbackArmed ||
				stage.RollbackSeconds < laneprovider.MinRollback || stage.RollbackSeconds > laneprovider.MaxRollback {
				if stage.TxID != "" {
					_, _ = laneService.Rollback(ctx, stage.TxID)
				}
				return result, fmt.Errorf("LANE staged apply missing valid timed rollback")
			}
			after, err := networkService.Snapshot(ctx)
			if err != nil {
				if stage.TxID != "" {
					if _, rollbackErr := laneService.Rollback(ctx, stage.TxID); rollbackErr != nil {
						return result, rollbackErr
					}
				}
				return result, fmt.Errorf("capture network state after LANE apply: %w; staged change rolled back", err)
			}
			result.After = networkops.Digest(after)
			result.Applied = true
			return result, nil
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "pending_commit"
			if err != nil {
				result = err.Error()
			} else if !out.Applied {
				result = "blocked"
			}
			_ = logger.Log(audit.Event{
				Type: "network_stage_apply", Result: result,
				Details: map[string]any{"kind": kind, "tx_id": out.Stage.TxID, "plan_id": out.Stage.PlanID},
			})
		}
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "network_stage_commit",
		Description: "Commit a pending LANE canary only after live LANE verification and MCPshka control-path verification succeed. Requires idempotency.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in NetworkStageTxInput) (*mcp.CallToolResult, NetworkStageCommitResult, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, NetworkStageCommitResult{}, errors.New("idempotency_key is required for network_stage_commit")
		}
		payload := struct{ TxID string }{strings.TrimSpace(in.TxID)}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "network_stage_commit", payload, func() (NetworkStageCommitResult, error) {
			status, err := laneService.Status(ctx)
			if err != nil {
				return NetworkStageCommitResult{}, err
			}
			if !status.Active || status.TxID != in.TxID || status.Status != "PENDING_COMMIT" || !status.RollbackArmed {
				return NetworkStageCommitResult{}, errors.New("LANE transaction is not pending commit with rollback armed")
			}
			verify, err := laneService.Verify(ctx)
			if err != nil {
				return NetworkStageCommitResult{Verify: verify}, err
			}
			if !verify.OK {
				return NetworkStageCommitResult{Verify: verify}, errors.New("LANE verification failed; rollback timer remains armed")
			}
			control, err := controlService.Snapshot(ctx)
			if err != nil {
				return NetworkStageCommitResult{Verify: verify}, err
			}
			if control.EmergencyState != "healthy" || control.Primary == "" {
				return NetworkStageCommitResult{Control: control, Verify: verify}, errors.New("control path is not healthy; rollback timer remains armed")
			}
			committed, err := laneService.Commit(ctx, in.TxID)
			return NetworkStageCommitResult{Control: control, Verify: verify, Result: committed}, err
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "network_stage_rollback",
		Description: "Request idempotent rollback of a LANE staged network transaction. This recovery path remains usable even when normal control-plane preflight would block new mutations.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in NetworkStageTxInput) (*mcp.CallToolResult, laneprovider.StageResult, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, laneprovider.StageResult{}, errors.New("idempotency_key is required for network_stage_rollback")
		}
		payload := struct{ TxID string }{strings.TrimSpace(in.TxID)}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "network_stage_rollback", payload, func() (laneprovider.StageResult, error) {
			return laneService.Rollback(ctx, in.TxID)
		})
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "network_snapshot",
		Description: "Return a bounded read-only network snapshot: links/addresses, routes, resolver configuration, listening sockets, VPN interface awareness and a stable state fingerprint.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, networkops.Snapshot, error) {
		out, err := networkService.Snapshot(ctx)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "network_connectivity",
		Description: "Run bounded fixed-target DNS and HTTPS connectivity probes without accepting arbitrary destinations.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EmptyInput) (*mcp.CallToolResult, networkops.Connectivity, error) {
		out, err := networkService.Connectivity(ctx)
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "endpoint_probe",
		Description: "Check from the host that its own tailnet/Funnel/LAN address answers: resolve through via=tailnet (MagicDNS), public or lan, then report DNS, TLS and HTTP status separately. Only *.ts.net names and private/tailnet/loopback addresses.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in EndpointProbeInput) (*mcp.CallToolResult, networkops.EndpointProbe, error) {
		out, err := networkops.ProbeEndpoint(ctx, in.URL, strings.TrimSpace(in.Via))
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "control_plane_https_probe",
		Description: "Read-only H96-side DNS and verified HTTPS diagnosis of fixed Tailscale controlplane/login endpoints. Optional dial_ip must match current public DNS A response; preserves hostname/SNI and full TLS certificate verification; no shell, proxy or redirects. H96 observation cannot prove iPhone LTE connectivity.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ControlPlaneHTTPSProbeInput) (*mcp.CallToolResult, networkops.ControlHTTPSProbe, error) {
		out, err := networkops.ProbeControlHTTPS(ctx, strings.TrimSpace(in.Target), strings.TrimSpace(in.DialIP))
		return nil, out, err
	}))

	add(tooldef.New(&mcp.Tool{
		Name:        "traffic_route_start",
		Description: "Start one bounded passive DIAG route-observation session for an explicitly identified LAN client through the PID-bound root broker: at most 300 s, one pcap hard-capped at 32 MiB (no ring overwrite), MAC re-checked during capture. Stop early with traffic_route_cancel. No route, firewall, DNS, DHCP or service configuration is changed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in TrafficRouteStartInput) (*mcp.CallToolResult, TrafficRouteStartResult, error) {
		client := strings.TrimSpace(in.Client)
		mac := strings.ToLower(strings.TrimSpace(in.MAC))
		if client == "" || mac == "" {
			return nil, TrafficRouteStartResult{}, errors.New("client and mac are required for traffic_route_start")
		}
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, TrafficRouteStartResult{}, errors.New("idempotency_key is required for traffic_route_start")
		}
		duration := in.DurationSeconds
		if duration == 0 {
			duration = 300
		}
		if duration < 10 || duration > 300 {
			return nil, TrafficRouteStartResult{}, errors.New("duration_seconds must be 10..300")
		}
		payload := struct {
			Client   string
			MAC      string
			Duration int
		}{Client: client, MAC: mac, Duration: duration}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "traffic_route_start", payload, func() (TrafficRouteStartResult, error) {
			raw, err := brokerclient.Run(ctx, "diag-traffic",
				"start", "--client", client, "--mac", mac,
				"--duration", fmt.Sprintf("%d", duration),
				"--root", "/var/lib/diag/traffic-runs",
			)
			if err != nil {
				return TrafficRouteStartResult{}, err
			}
			fields := strings.Fields(strings.TrimSpace(string(raw)))
			if len(fields) < 3 || fields[0] != "RUN" {
				return TrafficRouteStartResult{}, fmt.Errorf("unexpected DIAG traffic start response")
			}
			result := TrafficRouteStartResult{RunID: fields[1]}
			for _, field := range fields[2:] {
				parts := strings.SplitN(field, "=", 2)
				if len(parts) != 2 {
					continue
				}
				switch parts[0] {
				case "state":
					result.State = parts[1]
				case "pid":
					result.PID = parts[1]
				case "path":
					result.Path = parts[1]
				}
			}
			if result.RunID == "" || result.State == "" {
				return TrafficRouteStartResult{}, fmt.Errorf("incomplete DIAG traffic start response")
			}
			return result, nil
		})
		if logger := manager.AuditLogger(); logger != nil {
			result := "started"
			if err != nil {
				result = err.Error()
			}
			_ = logger.Log(audit.Event{
				Type: "traffic_route_start", Result: result,
				Details: map[string]any{"client": client, "run_id": out.RunID, "duration_seconds": duration},
			})
		}
		return nil, out, err
	}))

	trafficDoc := func(ctx context.Context, op, runID string) (TrafficRouteDocument, error) {
		raw, err := brokerclient.Run(ctx, "diag-traffic", op, "--run", runID, "--root", "/var/lib/diag/traffic-runs", "--json")
		if err != nil {
			return nil, err
		}
		var out TrafficRouteDocument
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decode DIAG traffic %s: %w", op, err)
		}
		return out, nil
	}
	for _, t := range []struct{ name, op, desc string }{
		{"traffic_route_status", "status", "Read bounded state for a DIAG traffic-route observation run through the privileged broker."},
		{"traffic_route_report", "report", "Return the bounded structured DIAG report for a completed traffic-route observation run."},
	} {
		t := t
		add(tooldef.New(&mcp.Tool{Name: t.name, Description: t.desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in TrafficRouteRunInput) (*mcp.CallToolResult, TrafficRouteDocument, error) {
			if strings.TrimSpace(in.RunID) == "" {
				return nil, nil, errors.New("run_id is required for " + t.name)
			}
			out, err := trafficDoc(ctx, t.op, strings.TrimSpace(in.RunID))
			return nil, out, err
		}))
	}
	add(tooldef.New(&mcp.Tool{
		Name:        "traffic_route_cancel",
		Description: "Stop a DIAG traffic-route run now (idempotent): the worker ends the capture; a dead or stuck worker and its tcpdump/conntrack groups are killed. Returns the state with ended_by, pcap_bytes and a cleanup receipt (all_stopped).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &nonDestructive, IdempotentHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in TrafficRouteCancelInput) (*mcp.CallToolResult, TrafficRouteDocument, error) {
		runID := strings.TrimSpace(in.RunID)
		if runID == "" || strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, nil, errors.New("run_id and idempotency_key are required for traffic_route_cancel")
		}
		out, err := idempotency.Run(idempotencyStore, in.IdempotencyKey, "traffic_route_cancel", runID, func() (TrafficRouteDocument, error) { return trafficDoc(ctx, "stop", runID) })
		if logger := manager.AuditLogger(); logger != nil {
			_ = logger.Log(audit.Event{Type: "traffic_route_cancel", Result: fmt.Sprint(out["state"]), Details: map[string]any{"run_id": runID}})
		}
		return nil, out, err
	}))
}

// ControlPlaneHTTPSProbeInput never accepts a raw URL or arbitrary path.
// Forced IPv4 is admitted only when it appears in the live DNS answer.
type ControlPlaneHTTPSProbeInput struct {
	Target string `json:"target" jsonschema:"tailscale_control or tailscale_login; two fixed approved HTTPS endpoints"`
	DialIP string `json:"dial_ip,omitempty" jsonschema:"optional literal public IPv4 from current target DNS answer; preserve hostname/SNI/TLS"`
}

// EndpointProbeInput names one of the host's own endpoints (MCP-116).
type EndpointProbeInput struct {
	URL string `json:"url" jsonschema:"http(s) URL of an own endpoint, for example https://h96.tail1234.ts.net:8447"`
	Via string `json:"via,omitempty" jsonschema:"tailnet (MagicDNS), public (as the internet sees a Funnel name) or lan; default tailnet for *.ts.net, else lan"`
}

type EmptyInput = toolcontract.EmptyInput
type NetworkStageApplyInput = toolcontract.NetworkStageApplyInput
type NetworkStageTxInput = toolcontract.NetworkStageTxInput
type NetworkStageApplyResult = toolcontract.NetworkStageApplyResult
type NetworkStageStatusResult = toolcontract.NetworkStageStatusResult
type NetworkStageCommitResult = toolcontract.NetworkStageCommitResult
type TrafficRouteStartInput = toolcontract.TrafficRouteStartInput
type TrafficRouteRunInput = toolcontract.TrafficRouteRunInput
type TrafficRouteCancelInput = toolcontract.TrafficRouteCancelInput
type TrafficRouteStartResult = toolcontract.TrafficRouteStartResult
type TrafficRouteDocument = toolcontract.TrafficRouteDocument

type Deps struct {
	Manager *jobs.Manager
	Store   *idempotency.Store
	Network *networkops.Service
	Control *controlplane.Service
	Lane    *laneprovider.Service
}
