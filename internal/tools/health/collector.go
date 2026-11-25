package health

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/diagprovider"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/hosttriage"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/knowledgeops"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/runtimehealth"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/systeminfo"
	"mcpshka/internal/tooldef"
	"strings"
	"time"
)

func Sample(state string, v any) (healthlayer.Sample, error) {
	raw, e := json.Marshal(v)
	return healthlayer.Sample{State: state, Data: raw}, e
}
func NewCollector(m *jobs.Manager, control *controlplane.Service, units *systemdsvc.Service, docker *dockerops.Service, lane *laneprovider.Service, extra []healthlayer.Probe, heal ...*selfheal.Service) *healthlayer.Collector {
	probes := []healthlayer.Probe{
		{Name: "control_path", Read: func(ctx context.Context) (healthlayer.Sample, error) {
			v, e := control.Snapshot(ctx)
			if e != nil {
				return healthlayer.Sample{}, e
			}
			state := "degraded"
			if v.EmergencyState == "healthy" && v.Primary != "" {
				state = "ok"
			}
			return Sample(state, map[string]any{"primary": v.Primary, "state": v.EmergencyState, "rollback_ready": v.RollbackReady, "captured_at": v.CapturedAt})
		}},
		{Name: "resources", Read: func(ctx context.Context) (healthlayer.Sample, error) {
			v, e := systeminfo.Read()
			if e != nil {
				return healthlayer.Sample{}, e
			}
			state := "ok"
			if v.MemoryTotalKB == 0 || v.DiskTotalBytes == 0 || v.LoadAverage == "" {
				state = "unknown"
			} else if v.MemoryAvailKB < 128*1024 || v.DiskFreeBytes < 3*1024*1024*1024 || v.DiskFreeBytes < v.DiskTotalBytes/10 || v.CPUTempC > 80 || v.Storage != nil && v.Storage.Warning {
				state = "degraded"
			}
			return Sample(state, v)
		}},
		{Name: "critical_services", Read: func(ctx context.Context) (healthlayer.Sample, error) {
			return CriticalServices(ctx, units.Status)
		}},
		{Name: "containers", Read: func(ctx context.Context) (healthlayer.Sample, error) {
			v, e := docker.List(ctx, 16)
			if e != nil {
				return healthlayer.Sample{}, e
			}
			state := "ok"
			summary := map[string]string{}
			for _, c := range v.Containers {
				if !healthlayer.CriticalContainer(c.Name) {
					continue
				}
				status := c.State
				if len(status) > 64 {
					status = "unknown"
				}
				// A clean exit with restart policy "no" is the owner's choice,
				// as host_triage already says; lampac kept health degraded.
				if hosttriage.StoppedOnPurpose(ctx, docker, c.State, c.Status, c.Name) {
					status = "stopped"
				} else if strings.Contains(strings.ToLower(c.Status), "unhealthy") {
					status = "unhealthy" // zigbee2mqtt read "running" while it was down
				}
				summary[c.Name] = status
				if status != "running" && status != "stopped" || strings.Contains(strings.ToLower(c.Status), "unhealthy") {
					state = "degraded"
				}
			}
			for _, name := range []string{"homeassistant", "zigbee2mqtt", "mosquitto"} {
				if _, ok := summary[name]; !ok {
					summary[name] = "missing"
				}
				if summary[name] != "running" {
					state = "degraded"
				}
			}
			if v.Truncated {
				state = "unknown"
			}
			data := map[string]any{"states": summary, "truncated": v.Truncated}
			if len(heal) > 0 && heal[0] != nil {
				sh, err := hosttriage.Summary(ctx, heal[0])
				data["self_heal"] = sh
				if err != nil {
					state = "unknown"
				} else if sh.Escalated > 0 {
					state = "degraded"
				}
			}
			return Sample(state, data)
		}},
		{Name: "jobs_workflow", Read: func(ctx context.Context) (healthlayer.Sample, error) { return runtimehealth.Read(ctx, m) }},
		{Name: "knowledge_core", Read: func(ctx context.Context) (healthlayer.Sample, error) {
			s, e := knowledge.FromDatabaseContext(ctx, m.Database())
			if e != nil {
				return healthlayer.Sample{}, e
			}
			revision, e := s.Revision(ctx)
			if e != nil {
				return healthlayer.Sample{}, e
			}
			return Sample("ok", map[string]any{"revision": revision, "authority": "canonical_sqlite", "records": "not_returned"})
		}},
		{Name: "search_index", Read: func(ctx context.Context) (healthlayer.Sample, error) {
			v, e := knowledgeops.IndexStatus(ctx, m)
			if e != nil {
				return healthlayer.Sample{}, e
			}
			state := "ok"
			if v.CapturedAt.IsZero() {
				state = "unknown"
			} else if !v.Ready || v.CapturedAt.After(time.Now().Add(time.Second)) {
				// Ready already means the index revision equals the knowledge
				// revision; age alone flagged an unchanged index stale.
				state = "stale"
			}
			sample, err := Sample(state, v)
			// Readiness is checked against the live revision on every read, so
			// a ready index is observed now; its build time made it stale after
			// 5 min without changes (BUG-20261023-84).
			sample.ObservedAt = v.CapturedAt
			if state == "ok" {
				sample.ObservedAt = time.Now()
			}
			sample.MaxAge = 5 * time.Minute
			return sample, err
		}},
		{Name: "lane_network", Read: func(ctx context.Context) (healthlayer.Sample, error) {
			v, e := lane.Health(ctx)
			if e != nil {
				return healthlayer.Sample{}, e
			}
			state := "unknown"
			if v.UpdatedAt.IsZero() {
				state = "unknown"
			} else if time.Since(v.UpdatedAt) > 5*time.Minute || v.UpdatedAt.After(time.Now().Add(time.Second)) {
				state = "stale"
			}
			sample, err := Sample(state, map[string]any{"snapshot": v, "network_state": "unknown", "interpretation": "freshness_only"})
			sample.ObservedAt = v.UpdatedAt
			sample.MaxAge = 5 * time.Minute
			return sample, err
		}},
		{Name: "diag", Read: func(ctx context.Context) (healthlayer.Sample, error) {
			v, e := diagprovider.NewNative(m).Health(ctx)
			if e != nil {
				return healthlayer.Sample{}, e
			}
			state := "degraded"
			if v.Ready {
				state = "ok"
			}
			return Sample(state, map[string]any{"version": v.Version, "schema": v.Schema, "ready": v.Ready})
		}},
	}
	for i := range probes {
		tool := "host_triage"
		switch probes[i].Name {
		case "control_path":
			tool = "control_path_status"
		case "knowledge_core":
			tool = "knowledge_health"
		case "search_index":
			tool = "index_health"
		case "lane_network":
			tool = "network_stage_status"
		case "diag":
			tool = "diag_health"
		}
		probes[i].NextSafeAction = Action(tool, map[string]any{})
	}
	return healthlayer.New(append(probes, extra...))
}
func Register(add func(tooldef.Def), collector *healthlayer.Collector) {
	add(tooldef.New(&mcp.Tool{Name: "health_snapshot", OutputSchema: OutputSchema(), Description: "Bounded on-demand read-only P8 aggregate. Fixed sources, one concurrent collection, 15s cache, 16s collection deadline and 16KiB output. Missing/stale data stays explicit; consecutive failures are process-local. Includes a compact mission progress/blocker summary, broker version, bounded executor permit/unit age and separate Codex/Claude authorization/quota evidence. Every non-ok source includes freshness and a read-only next_safe_action. Aggregate degradation does not change individual tunnel/Docker states. LANE health is read without probes or network changes; DIAG readiness comes from its fixed capabilities/version CLI. No background monitor, retries or repairs.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, healthlayer.Report, error) {
		out, e := collector.Read(ctx)
		return nil, out, e
	}))
}

// The MCP SDK infers json.RawMessage as []byte (an array), although each
// source supplies validated JSON. Declare the protocol schema explicitly.
func OutputSchema() map[string]any {
	fields := map[string]any{
		"baseline": map[string]any{"type": "array", "maxItems": 6, "items": map[string]any{"type": "object"}},
		"trust":    map[string]any{"type": "string", "enum": []string{"UNTRUSTED"}},
		"state":    map[string]any{"type": "string", "enum": []string{"ok", "degraded", "unknown", "stale"}},
		"counts": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"ok", "degraded", "unknown", "stale"}, "properties": map[string]any{
			"ok":       map[string]any{"type": "integer", "minimum": 0, "maximum": healthlayer.MaxProbes},
			"degraded": map[string]any{"type": "integer", "minimum": 0, "maximum": healthlayer.MaxProbes},
			"unknown":  map[string]any{"type": "integer", "minimum": 0, "maximum": healthlayer.MaxProbes},
			"stale":    map[string]any{"type": "integer", "minimum": 0, "maximum": healthlayer.MaxProbes},
		}},
		"captured_at":            map[string]any{"type": "string"},
		"cached":                 map[string]any{"type": "boolean"},
		"age_ms":                 map[string]any{"type": "integer"},
		"duration_ms":            map[string]any{"type": "integer"},
		"cache_ttl_ms":           map[string]any{"type": "integer"},
		"collection_deadline_ms": map[string]any{"type": "integer"},
		"items": map[string]any{"type": "array", "minItems": 1, "maxItems": healthlayer.MaxProbes, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "state", "captured_at", "consecutive_failures", "freshness"}, "properties": map[string]any{
			"name":                 map[string]any{"type": "string", "maxLength": 64},
			"state":                map[string]any{"type": "string", "enum": []string{"ok", "degraded", "unknown", "stale"}},
			"captured_at":          map[string]any{"type": "string"},
			"consecutive_failures": map[string]any{"type": "integer", "minimum": 0, "maximum": 255},
			"freshness": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"observed_at", "age_ms", "max_age_ms"}, "properties": map[string]any{
				"observed_at": map[string]any{"type": "string"}, "age_ms": map[string]any{"type": "integer"}, "max_age_ms": map[string]any{"type": "integer"},
			}},
			"next_safe_action": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"tool", "args", "approval"}, "properties": map[string]any{
				"tool": map[string]any{"type": "string"}, "args": map[string]any{"type": "object"}, "approval": map[string]any{"type": "string"},
			}},
			"data": map[string]any{},
		}}},
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"trust", "state", "counts", "captured_at", "cached", "age_ms", "duration_ms", "cache_ttl_ms", "collection_deadline_ms", "items"}, "properties": fields}
}
