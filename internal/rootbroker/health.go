package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/dockersmoke"
	"mcpshka/internal/executor"
	"mcpshka/internal/unitbus"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BrokerVersion identifies this running broker image, never a file on disk.
// The controller test enforces equality with its release version literal.
const BrokerVersion = "0.19.1"

var brokerImageSHA = sync.OnceValue(func() string { return fileSHA("/proc/self/exe") })

func runHealth(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 1 {
		return nil, errors.New("health requires one fixed source")
	}
	// ~70 permit records took over 1s at load 7: the scan stopped at 24 and
	// the executor read UNKNOWN with no reason (BUG-20261023-85).
	child, cancel := context.WithTimeout(ctx, 4000*time.Millisecond)
	defer cancel()
	switch args[0] {
	case "broker":
		return json.Marshal(map[string]any{"version": BrokerVersion, "build_sha256": brokerImageSHA(), "captured_at": time.Now().UTC()})
	case "executor":
		permits, err := executor.HealthAuthority(child)
		if err != nil {
			permits.State = "unknown"
			permits.Reason = "permit scan incomplete: " + err.Error()
		}
		// Own budget: a slow permit scan expired the shared context and the
		// unit probe always read unknown.
		unitCtx, unitCancel := context.WithTimeout(ctx, 800*time.Millisecond)
		defer unitCancel()
		units := executorUnitsHealth(unitCtx)
		return json.Marshal(map[string]any{"permits": permits, "units": units, "recovery": recoveryHealth(executor.IntegrationMaintenanceFailures())})
	case "docker_smoke":
		return json.Marshal(dockersmoke.SelfTest(child))
	case "host_events":
		return json.Marshal(hostEvents(child))
	default:
		return nil, errors.New("unsupported health source")
	}
}

type executorUnitHealth struct {
	State        string `json:"state"`
	Checked      int    `json:"checked"`
	Overdue      int    `json:"overdue"`
	Unknown      int    `json:"unknown"`
	GraceSeconds int    `json:"grace_seconds"`
}

func executorUnitsHealth(ctx context.Context) executorUnitHealth {
	unknown := executorUnitHealth{State: "unknown", GraceSeconds: 60}
	text, err := unitbus.Bounded(ctx, 16384, "show", "--property=Id,ActiveState,ExecMainStartTimestampMonotonic,RuntimeMaxUSec", "mcpshka-executor-*.service")
	if err != nil {
		return unknown
	}
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return unknown
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return unknown
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || uptime <= 0 {
		return unknown
	}
	return parseExecutorUnits(string(text), uint64(uptime*1e6))
}

func parseExecutorUnits(raw string, uptimeUS uint64) executorUnitHealth {
	r := executorUnitHealth{State: "ok", GraceSeconds: 60}
	for _, block := range strings.Split(strings.TrimSpace(raw), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		props := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				r.Unknown++
				continue
			}
			props[k] = v
		}
		id := props["Id"]
		if !strings.HasPrefix(id, "mcpshka-executor-") || !strings.HasSuffix(id, ".service") || !executorID.MatchString(strings.TrimSuffix(strings.TrimPrefix(id, "mcpshka-executor-"), ".service")) {
			r.Unknown++
			continue
		}
		r.Checked++
		if r.Checked > 32 {
			r.Unknown++
			break
		}
		switch props["ActiveState"] {
		case "inactive":
			continue
		case "failed":
			r.Overdue++
			continue
		case "active", "activating", "deactivating":
		default:
			r.Unknown++
			continue
		}
		start, e := strconv.ParseUint(props["ExecMainStartTimestampMonotonic"], 10, 64)
		limit, e2 := parseRuntimeUS(props["RuntimeMaxUSec"])
		if e != nil || e2 != nil || start == 0 || start > uptimeUS {
			r.Unknown++
			continue
		}
		if uptimeUS-start > limit && uptimeUS-start-limit > 60000000 {
			r.Overdue++
		}
	}
	switch {
	case r.Overdue > 0:
		r.State = "degraded"
	case r.Unknown > 0:
		r.State = "unknown"
	}
	return r
}

func parseRuntimeUS(value string) (uint64, error) {
	// systemctl prints timespans (e.g. "30min"), not raw microseconds.
	d, err := time.ParseDuration(strings.ReplaceAll(strings.ReplaceAll(value, "min", "m"), " ", ""))
	if err != nil || d <= 0 {
		return 0, errors.New("executor runtime limit unavailable")
	}
	return uint64(d / time.Microsecond), nil
}

// recoveryHealth turns consecutive failed integration recovery sweeps into a
// health state: one slow sweep is noise, three in a row (~90 s) is degraded
// (DIAG ACTIVE-01: health said OK while recovery failed for hours).
func recoveryHealth(failed int64) map[string]any {
	state := "ok"
	if failed >= 3 {
		state = "degraded"
	}
	return map[string]any{"state": state, "failed_sweeps": failed}
}
