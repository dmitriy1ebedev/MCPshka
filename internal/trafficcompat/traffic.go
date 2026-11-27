package trafficcompat

import (
	"context"
	"errors"
	"fmt"
	"mcpshka/internal/audit"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/toolcontract"
	"strings"
	"time"
)

type Plan struct {
	Operation      string
	BrokerArgs     []string
	IdempotencyKey string
}

func Parse(command string, args []string) (Plan, bool, error) {
	command = strings.TrimSpace(command)
	if (command != "mcpshka" && command != "/usr/local/bin/mcpshka") || len(args) == 0 || args[0] != "traffic-route" {
		return Plan{}, false, nil
	}
	tail := args[1:]
	if len(tail) == 0 {
		return Plan{}, true, errors.New("usage: mcpshka traffic-route [start|status|report] ...")
	}
	switch tail[0] {
	case "start":
		if len(tail) != 9 || tail[1] != "--client" || tail[3] != "--mac" || tail[5] != "--duration" || tail[7] != "--request-id" {
			return Plan{}, true, errors.New("usage: mcpshka traffic-route start --client <ipv4> --mac <mac> --duration <10..900> --request-id <id>")
		}
		requestID := strings.TrimSpace(tail[8])
		if requestID == "" || len(requestID) > 128 || strings.ContainsAny(requestID, "\x00\r\n\t") {
			return Plan{}, true, errors.New("invalid traffic-route request id")
		}
		return Plan{
			Operation: "start",
			BrokerArgs: []string{
				"start", "--client", tail[2], "--mac", tail[4], "--duration", tail[6],
				"--root", "/var/lib/diag/traffic-runs",
			},
			IdempotencyKey: "compat-traffic-route:" + requestID,
		}, true, nil
	case "status", "report":
		if len(tail) != 3 || tail[1] != "--run" {
			return Plan{}, true, fmt.Errorf("usage: mcpshka traffic-route %s --run <run_id>", tail[0])
		}
		return Plan{
			Operation:  tail[0],
			BrokerArgs: []string{tail[0], "--run", tail[2], "--root", "/var/lib/diag/traffic-runs", "--json"},
		}, true, nil
	default:
		return Plan{}, true, fmt.Errorf("unsupported traffic-route operation %q", tail[0])
	}
}

func Run(
	ctx context.Context,
	manager *jobs.Manager,
	store *idempotency.Store,
	in toolcontract.ExecStartInput,
	plan Plan,
) (jobs.Snapshot, error) {
	if len(in.Env) != 0 || strings.TrimSpace(in.ApprovalToken) != "" {
		return jobs.Snapshot{}, errors.New("traffic-route compatibility shim does not accept env or approval_token")
	}
	cwd, err := manager.Policy().CheckWorkdir(in.Workdir)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if _, err := manager.Policy().DecideCommandForWorkdir(in.Command, in.Args, cwd); err != nil {
		return jobs.Snapshot{}, err
	}

	runBroker := func() (string, error) {
		raw, err := brokerclient.Run(ctx, "diag-traffic", plan.BrokerArgs...)
		return string(raw), err
	}
	var output string
	if plan.Operation == "start" {
		payload := struct {
			BrokerArgs []string
		}{BrokerArgs: plan.BrokerArgs}
		output, err = idempotency.Run(store, plan.IdempotencyKey, "compat_traffic_route_start", payload, runBroker)
	} else {
		output, err = runBroker()
	}
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if logger := manager.AuditLogger(); logger != nil {
		_ = logger.Log(audit.Event{
			Type: "compat_traffic_route", Command: in.Command, Args: in.Args, Workdir: cwd, Result: "ok",
			Details: map[string]any{"operation": plan.Operation},
		})
	}
	return manager.StartWithOptions(
		"/usr/bin/printf",
		[]string{"%s", output},
		cwd,
		jobs.StartOptions{
			OutputLimitBytes: 128 * 1024,
			Timeout:          10 * time.Second,
			MemoryLimitBytes: 64 * 1024 * 1024,
			CPUTimeLimit:     5 * time.Second,
			ProcessLimit:     4,
			CPUQuotaPercent:  100,
		},
	)
}
