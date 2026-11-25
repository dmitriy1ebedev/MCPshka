package health

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"mcpshka/internal/agents"
	"mcpshka/internal/deploy"
	"mcpshka/internal/executor"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/systemdsvc"
)

func Action(tool string, args map[string]any) *healthlayer.Action {
	raw, _ := json.Marshal(args)
	return &healthlayer.Action{Tool: tool, Args: raw, Approval: "none"}
}

// Each source keeps its own state: a LANE/index/provider failure cannot change
// the observed tunnel or Docker status. Disabled laned remains intentional.
func CriticalServices(ctx context.Context, read func(context.Context, string) (systemdsvc.Status, error)) (healthlayer.Sample, error) {
	state := "ok"
	summary := map[string]string{}
	for _, name := range []string{"mcpshka.service", "mcpshka-tunnel.service", "laned.service", "docker.service"} {
		v, err := read(ctx, name)
		if err != nil {
			summary[name] = "unknown"
			if state != "degraded" {
				state = "unknown"
			}
			continue
		}
		summary[name] = v.ActiveState
		if name == "laned.service" && v.ActiveState == "inactive" && v.UnitFileState == "disabled" {
			summary[name] = "disabled"
			continue
		}
		if v.ActiveState != "active" {
			state = "degraded"
		}
	}
	return Sample(state, summary)
}

type BrokerRunner func(context.Context, string, ...string) ([]byte, error)

var executorCanaryID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func BlockerProbes(run BrokerRunner, version string) []healthlayer.Probe {
	return []healthlayer.Probe{
		{Name: "broker", NextSafeAction: Action("service_status", map[string]any{"service": "mcpshka-broker.service"}), Read: func(ctx context.Context) (healthlayer.Sample, error) {
			raw, err := run(ctx, "health", "broker")
			if err != nil {
				return healthlayer.Sample{}, err
			}
			var v struct {
				Version    string    `json:"version"`
				CapturedAt time.Time `json:"captured_at"`
			}
			if len(raw) > 1024 || json.Unmarshal(raw, &v) != nil || len(v.Version) > 64 {
				return healthlayer.Sample{State: "unknown"}, nil
			}
			state := "unknown"
			if v.Version != "" && !v.CapturedAt.IsZero() {
				state = "degraded"
				if v.Version == version {
					state = "ok"
				}
			}
			sample, err := Sample(state, map[string]any{"version": v.Version, "controller_version": version})
			sample.ObservedAt = v.CapturedAt
			sample.MaxAge = 5 * time.Minute
			if state == "degraded" {
				sample.Action = Action("tool_resolve", map[string]any{"goal": "broker controller version mismatch", "compact": true})
			}
			return sample, err
		}},
		{Name: "executor", NextSafeAction: Action("host_triage", map[string]any{}), Timeout: 6 * time.Second, Read: func(ctx context.Context) (healthlayer.Sample, error) {
			raw, err := run(ctx, "health", "executor")
			if err != nil {
				return healthlayer.Sample{}, err
			}
			var v struct {
				Permits executor.PermitHealth `json:"permits"`
				Units   struct {
					State string `json:"state"`
				} `json:"units"`
				Recovery struct {
					State string `json:"state"`
				} `json:"recovery"`
			}
			if len(raw) > 4096 || json.Unmarshal(raw, &v) != nil {
				return healthlayer.Sample{State: "unknown"}, nil
			}
			state := States(v.Permits.State, v.Units.State)
			// An older broker omits recovery; only an explicit degraded counts.
			if v.Recovery.State == "degraded" {
				state = "degraded"
			}
			if v.Permits.CapturedAt.IsZero() && state != "degraded" {
				state = "unknown"
			}
			sample := healthlayer.Sample{State: state, Data: raw, ObservedAt: v.Permits.CapturedAt, MaxAge: 5 * time.Minute}
			if executorCanaryID.MatchString(v.Permits.PermitID) && executorCanaryID.MatchString(v.Permits.LaunchID) {
				sample.Action = Action("executor_runtime_status", map[string]any{"permit_id": v.Permits.PermitID, "launch_id": v.Permits.LaunchID, "workdir": deploy.DefaultRepoRoot})
			}
			return sample, nil
		}},
		{Name: "providers", NextSafeAction: Action("mcpshka_guide", map[string]any{"topic": "providers"}), Read: func(ctx context.Context) (healthlayer.Sample, error) {
			v := agents.Health(ctx, time.Now().UTC())
			return Sample(States(v[0].State, v[1].State), v)
		}},
	}
}

func States(states ...string) string {
	result := "ok"
	for _, s := range states {
		switch s {
		case "degraded":
			return "degraded"
		case "ok":
		case "stale":
			if result == "ok" {
				result = "stale"
			}
		default:
			result = "unknown"
		}
	}
	return result
}
