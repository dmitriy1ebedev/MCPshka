package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/healthlayer"
)

// Readiness states. A registered capability has no observation yet; preflight
// means its evidence is missing or stale; the rest come from observed health
// sources and the capability's own self-test, run as the UID that serves it.
const (
	Registered = "registered"
	Preflight  = "preflight"
	Ready      = "ready"
	Degraded   = "degraded"
	Blocked    = "blocked"
)

// readinessEvidenceAge bounds how old health evidence may be before
// tool_resolve stops trusting it and reports preflight instead.
const readinessEvidenceAge = 10 * time.Minute

type CapabilityDependency struct {
	Name           string              `json:"name"`
	State          string              `json:"state"`
	Required       bool                `json:"required"`
	NextSafeAction *healthlayer.Action `json:"next_safe_action,omitempty"`
}

type CapabilityReadiness struct {
	Name           string                 `json:"name"`
	State          string                 `json:"state"`
	BackendHealthy string                 `json:"backend_healthy"`
	Effect         string                 `json:"effect"`
	Privilege      string                 `json:"privilege"`
	Directories    []string               `json:"directories,omitempty"`
	Limits         string                 `json:"limits,omitempty"`
	Recovery       string                 `json:"recovery,omitempty"`
	Dependencies   []CapabilityDependency `json:"dependencies"`
	SelfTest       json.RawMessage        `json:"self_test,omitempty"`
	ObservedAt     string                 `json:"observed_at,omitempty"`
	Reason         string                 `json:"reason,omitempty"`
}

// capabilityContract is what a capability needs to do its job. Only the
// fields that differ from the defaults are spelled out per tool.
type capabilityContract struct {
	privilege   string
	directories []string
	limits      string
	recovery    string
	// required health sources: the capability cannot work while one is
	// degraded. optional ones only degrade it.
	required, optional []string
	selfTest           string
}

// capabilityContracts is ordered: the first prefix that matches wins, so
// specific tools precede their family.
var capabilityContracts = []struct {
	prefix string
	c      capabilityContract
}{
	{"docker_smoke", capabilityContract{privilege: "root_broker", directories: []string{"/var/lib/mcpshka-executor/docker-smoke", "/var/lib/mcpshka/heavy-queue"},
		limits: "worker unit MemoryMax=256M CPUQuota=25% RuntimeMaxSec=3h; one build at a time through the heavy queue", recovery: "docker_smoke_status reads the result; image, volume and containers are removed by the worker",
		required: []string{"broker", "critical_services"}, optional: []string{"containers"}, selfTest: "docker_smoke"}},
	{"docker_", capabilityContract{privilege: "root_broker", required: []string{"broker", "critical_services"}, optional: []string{"containers"}}},
	{"project_build", capabilityContract{privilege: "executor_sandbox", limits: "heavy queue; profile timeout", recovery: "job_status/job_wait; executor_admin_stale_preview for orphaned permits", required: []string{"executor", "broker"}, optional: []string{"resources"}}},
	{"project_pipeline", capabilityContract{privilege: "executor_sandbox", limits: "heavy queue; profile timeout", recovery: "job_status/job_wait; executor_admin_stale_preview for orphaned permits", required: []string{"executor", "broker"}, optional: []string{"resources"}}},
	{"project_test", capabilityContract{privilege: "executor_sandbox", limits: "heavy queue; profile timeout", recovery: "job_status/job_wait; executor_admin_stale_preview for orphaned permits", required: []string{"executor", "broker"}, optional: []string{"resources"}}},
	{"project_run", capabilityContract{privilege: "executor_sandbox", recovery: "job_status/job_wait", required: []string{"executor", "broker"}, optional: []string{"resources"}}},
	{"project_deploy", capabilityContract{privilege: "root_broker", recovery: "project_deploy_status, project_deploy_reconcile", required: []string{"broker"}, optional: []string{"jobs_workflow"}}},
	{"exec_", capabilityContract{privilege: "service_uid", recovery: "job_status/job_output", required: []string{"jobs_workflow"}, optional: []string{"resources"}}},
	{"job_", capabilityContract{privilege: "service_uid", required: []string{"jobs_workflow"}}},
	{"workflow_", capabilityContract{privilege: "service_uid", required: []string{"jobs_workflow"}}},
	{"release_", capabilityContract{privilege: "root_broker", recovery: "release_status, release_apply_status; rollback to the previous release", required: []string{"broker", "executor"}, optional: []string{"resources"}}},
	{"integration_", capabilityContract{privilege: "service_uid", recovery: "integration_recovery_status, integration_recover", required: []string{"broker"}, optional: []string{"providers", "resources"}}},
	{"forge_ci", capabilityContract{privilege: "service_uid", required: []string{"control_path"}, optional: []string{"providers"}}},
	{"agent_task_cancel", capabilityContract{privilege: "face_id", recovery: "agent_task_status", required: []string{"broker"}}},
	{"agent_task", capabilityContract{privilege: "face_id", recovery: "agent_task_status; resume_of after a reboot", required: []string{"broker"}, optional: []string{"providers", "resources"}}},
	{"codex_", capabilityContract{privilege: "service_uid", required: []string{"providers"}}},
	{"cloud_", capabilityContract{privilege: "service_uid", optional: []string{"providers", "control_path"}}},
	{"knowledge_", capabilityContract{privilege: "service_uid", required: []string{"knowledge_core"}}},
	{"search_reindex", capabilityContract{privilege: "service_uid", required: []string{"search_index"}}},
	{"index_health", capabilityContract{privilege: "service_uid", required: []string{"search_index"}}},
	{"diag_", capabilityContract{privilege: "root_broker", required: []string{"diag"}, optional: []string{"broker"}}},
	{"network_", capabilityContract{privilege: "root_broker", recovery: "network_stage_status, network_stage_rollback", required: []string{"broker"}, optional: []string{"lane_network"}}},
	{"service_", capabilityContract{privilege: "root_broker", required: []string{"broker"}, optional: []string{"critical_services"}}},
	{"journal_read", capabilityContract{privilege: "root_broker", required: []string{"broker"}}},
	{"storage_", capabilityContract{privilege: "root_broker", required: []string{"broker"}}},
	{"backup_", capabilityContract{privilege: "root_broker", required: []string{"broker"}}},
	{"state_", capabilityContract{privilege: "root_broker", required: []string{"broker"}}},
	{"approved_task", capabilityContract{privilege: "face_id", directories: []string{"/var/lib/mcpshka-executor/approved"}, limits: "one run per Face ID; MemoryMax=512M TasksMax=128; no shell", recovery: "approved_task_status", required: []string{"broker"}}},
	{"root_task", capabilityContract{privilege: "face_id", recovery: "root_task_status", required: []string{"broker"}}},
	{"admin_session", capabilityContract{privilege: "face_id", required: []string{"broker"}}},
	{"mission_", capabilityContract{privilege: "service_uid", required: []string{"mission"}}},
	{"roadmap_run", capabilityContract{privilege: "face_id", recovery: "roadmap_run_status", required: []string{"jobs_workflow"}, optional: []string{"providers", "resources"}}},
	{"control_path", capabilityContract{privilege: "service_uid", required: []string{"control_path"}}},
	{"executor_", capabilityContract{privilege: "root_broker", required: []string{"broker", "executor"}}},
	{"host_diagnose", capabilityContract{privilege: "service_uid", required: []string{"broker"}, optional: []string{"resources"}}},
	{"host_", capabilityContract{privilege: "service_uid", optional: []string{"resources", "broker"}}},
	{"health_snapshot", capabilityContract{privilege: "service_uid"}},
	{"ha_", capabilityContract{privilege: "service_uid", required: []string{"critical_services"}}},
	{"tool_forge", capabilityContract{privilege: "face_id", directories: []string{"/var/lib/mcpshka/toolforge"}}},
}

// Declared reports whether name has a readiness contract.
func Declared(name string) bool {
	_, ok := contractFor(name)
	return ok
}

func contractFor(name string) (capabilityContract, bool) {
	for _, e := range capabilityContracts {
		if strings.HasPrefix(name, e.prefix) {
			return e.c, true
		}
	}
	return capabilityContract{privilege: "service_uid"}, false
}

type selfTestEntry struct {
	state string
	raw   json.RawMessage
	at    time.Time
}

// capabilitySelfTests run a capability's own preconditions as the UID that
// serves it. They are on demand only (capability_selftest/preflight).
var capabilitySelfTests = map[string]func(context.Context) (string, json.RawMessage, error){
	"docker_smoke": func(ctx context.Context) (string, json.RawMessage, error) {
		raw, err := brokerclient.Run(ctx, "health", "docker_smoke")
		if err != nil {
			return "", nil, err
		}
		var v struct {
			State string `json:"state"`
		}
		if len(raw) > 4096 || json.Unmarshal(raw, &v) != nil {
			return "", nil, errors.New("malformed docker_smoke self-test")
		}
		switch v.State {
		case Ready, Degraded, Blocked:
			return v.State, raw, nil
		}
		return "", nil, errors.New("unknown docker_smoke self-test state")
	},
}

type Cache struct {
	mu    sync.Mutex
	tests map[string]selfTestEntry
}

func (c *Cache) get(name string) (selfTestEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.tests[name]
	if !ok || time.Since(e.at) > readinessEvidenceAge {
		return selfTestEntry{}, false
	}
	return e, true
}

func (c *Cache) put(name string, e selfTestEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tests == nil {
		c.tests = map[string]selfTestEntry{}
	}
	c.tests[name] = e
}

// evaluateReadiness derives one capability's state from a health report
// (nil when no fresh one exists) and its cached self-test. Fail-closed: a
// missing, unknown or stale source never yields ready.
func evaluateReadiness(name, effect string, report *healthlayer.Report, test *selfTestEntry) CapabilityReadiness {
	c, known := contractFor(name)
	out := CapabilityReadiness{Name: name, Effect: effect, Privilege: c.privilege, Directories: c.directories, Limits: c.limits, Recovery: c.recovery, Dependencies: []CapabilityDependency{}}
	if !known {
		out.State, out.BackendHealthy, out.Reason = Registered, "unknown", "no declared backend dependencies; only registration is known"
		return out
	}
	items := map[string]healthlayer.Item{}
	if report != nil {
		for _, it := range report.Items {
			items[it.Name] = it
		}
		out.ObservedAt = report.CapturedAt.UTC().Format(time.RFC3339)
	}
	state := Ready
	worse := func(s string) {
		rank := map[string]int{Ready: 0, Preflight: 1, Degraded: 2, Blocked: 3}
		if rank[s] > rank[state] {
			state = s
		}
	}
	for _, dep := range append(append([]string{}, c.required...), c.optional...) {
		required := slices.Contains(c.required, dep)
		d := CapabilityDependency{Name: dep, State: "unknown", Required: required}
		if it, ok := items[dep]; ok {
			d.State, d.NextSafeAction = it.State, it.NextSafeAction
		}
		out.Dependencies = append(out.Dependencies, d)
		switch {
		case d.State == "ok":
		case d.State == "degraded" && required:
			worse(Blocked)
			if out.Reason == "" {
				out.Reason = "required dependency " + dep + " is degraded"
			}
		case d.State == "degraded":
			worse(Degraded)
		default:
			worse(Preflight)
		}
	}
	if c.selfTest != "" {
		if test == nil {
			worse(Preflight)
			if out.Reason == "" {
				out.Reason = "self-test not run yet: capability_selftest names=[\"" + name + "\"]"
			}
		} else {
			out.SelfTest = test.raw
			worse(test.state)
			if test.state == Blocked && out.Reason == "" {
				out.Reason = "self-test blocked"
			}
		}
	}
	if report == nil && out.Reason == "" {
		out.Reason = "no fresh health evidence: run capability_selftest or health_snapshot"
	}
	out.State = state
	switch state {
	case Ready:
		out.BackendHealthy = "true"
	case Degraded, Blocked:
		out.BackendHealthy = "false"
	default:
		out.BackendHealthy = "unknown"
	}
	return out
}

// cachedReadiness is the fast path for tool_resolve and the bootstrap:
// evidence that already exists, never a probe.
func (r *Engine) Cached(name string) CapabilityReadiness {
	var report *healthlayer.Report
	if r.Health != nil {
		if rep, ok := r.Health.Last(readinessEvidenceAge); ok {
			report = &rep
		}
	}
	var test *selfTestEntry
	if e, ok := r.Cache.get(name); ok {
		test = &e
	}
	return evaluateReadiness(name, r.Effect(name), report, test)
}

// probeReadiness collects health now (15s shared cache) and reruns the named
// capabilities' self-tests, caching them for later fast reads.
func (r *Engine) Probe(ctx context.Context, names []string) ([]CapabilityReadiness, error) {
	var report *healthlayer.Report
	if r.Health != nil {
		rep, err := r.Health.Read(ctx)
		if err != nil {
			return nil, err
		}
		report = &rep
	}
	out := make([]CapabilityReadiness, 0, len(names))
	for _, name := range names {
		var test *selfTestEntry
		if c, _ := contractFor(name); c.selfTest != "" {
			if run, ok := capabilitySelfTests[c.selfTest]; ok {
				tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				state, raw, err := run(tctx)
				cancel()
				e := selfTestEntry{state: state, raw: raw, at: time.Now()}
				if err != nil {
					e.state = Blocked
					e.raw, _ = json.Marshal(map[string]string{"capability": c.selfTest, "state": Blocked, "error": r.CompactError(err.Error())})
				}
				r.Cache.put(name, e)
				test = &e
			}
		}
		out = append(out, evaluateReadiness(name, r.Effect(name), report, test))
	}
	return out, nil
}

type CapabilitySelfTestInput struct {
	Names []string `json:"names" jsonschema:"1..16 registered tool names to check now (health sources + own self-test as the serving UID)."`
}

type CapabilitySelfTestResult struct {
	Capabilities []CapabilityReadiness `json:"capabilities"`
	Unknown      []string              `json:"unknown,omitempty"`
}

type CapabilityPreflightInput struct {
	Step         string   `json:"step,omitempty" jsonschema:"Roadmap step this preflight is for (free text, echoed back)."`
	Capabilities []string `json:"capabilities" jsonschema:"1..32 tool names the step will call."`
}

type PreflightStage struct {
	Stage   string   `json:"stage"`
	Verdict string   `json:"verdict"`
	Details []string `json:"details,omitempty"`
}

type CapabilityPreflightResult struct {
	Step           string                `json:"step,omitempty"`
	Verdict        string                `json:"verdict"`
	Stages         []PreflightStage      `json:"stages"`
	Capabilities   []CapabilityReadiness `json:"capabilities"`
	Blocker        string                `json:"blocker,omitempty"`
	NextSafeAction *healthlayer.Action   `json:"next_safe_action,omitempty"`
}

func ValidNames(names []string, max int) error {
	if len(names) == 0 || len(names) > max {
		return errors.New("names must list 1.." + strconv.Itoa(max) + " tool names")
	}
	for _, n := range names {
		if n == "" || len(n) > 64 {
			return errors.New("tool names must be 1..64 bytes")
		}
	}
	return nil
}

// preflight walks a roadmap step's dependencies in order — registration,
// rights, backend dependencies and self-tests, recovery — and stops at the
// first stage that blocks so the caller repairs it before implementing.
func (r *Engine) Preflight(ctx context.Context, in CapabilityPreflightInput) (CapabilityPreflightResult, error) {
	out := CapabilityPreflightResult{Step: in.Step, Stages: []PreflightStage{}, Capabilities: []CapabilityReadiness{}}
	if err := ValidNames(in.Capabilities, 32); err != nil {
		return out, err
	}
	stage := func(name string, details []string, verdict string) {
		out.Stages = append(out.Stages, PreflightStage{Stage: name, Verdict: verdict, Details: details})
	}
	block := func(reason string, action *healthlayer.Action) {
		out.Verdict, out.Blocker, out.NextSafeAction = Blocked, reason, action
	}
	var missing, denied, known []string
	for _, n := range in.Capabilities {
		ok, allowed := r.Available(ctx, n)
		switch {
		case !ok:
			missing = append(missing, n)
		case !allowed:
			denied = append(denied, n)
		default:
			known = append(known, n)
		}
	}
	if len(missing) > 0 {
		stage("capabilities", missing, "fail")
		block("not registered: "+strings.Join(missing, ", "), healthAction("tool_resolve", map[string]any{"goal": missing[0], "compact": true}))
		return out, nil
	}
	stage("capabilities", nil, "pass")
	if len(denied) > 0 {
		stage("rights", denied, "fail")
		block("not authorized for this caller: "+strings.Join(denied, ", "), healthAction("mcpshka_guide", map[string]any{"topic": "rules"}))
		return out, nil
	}
	stage("rights", nil, "pass")
	caps, err := r.Probe(ctx, known)
	if err != nil {
		return out, err
	}
	out.Capabilities = caps
	out.Verdict = Ready
	var bad []string
	for _, c := range caps {
		if c.State == Ready || c.State == Registered {
			continue
		}
		bad = append(bad, c.Name+"="+c.State)
		if c.State == Blocked && out.Blocker == "" {
			var action *healthlayer.Action
			for _, d := range c.Dependencies {
				if d.Required && d.State != "ok" && d.NextSafeAction != nil {
					action = d.NextSafeAction
					break
				}
			}
			if action == nil {
				action = healthAction("capability_selftest", map[string]any{"names": []string{c.Name}})
			}
			block(c.Name+": "+c.Reason, action)
		} else if out.Verdict == Ready {
			out.Verdict = c.State
		}
	}
	switch {
	case out.Verdict == Blocked:
		stage("dependencies", bad, "fail")
		return out, nil
	case len(bad) > 0:
		stage("dependencies", bad, "warn")
	default:
		stage("dependencies", nil, "pass")
	}
	// Recovery: unfinished work from an earlier run must be reconciled
	// before new mutations; the mission and executor sources carry it.
	recovery := "pass"
	var notes []string
	if r.Health != nil {
		if rep, ok := r.Health.Last(readinessEvidenceAge); ok {
			for _, it := range rep.Items {
				if (it.Name == "mission" || it.Name == "executor" || it.Name == "jobs_workflow") && it.State != "ok" {
					recovery = "warn"
					notes = append(notes, it.Name+"="+it.State)
					if out.NextSafeAction == nil {
						out.NextSafeAction = it.NextSafeAction
					}
				}
			}
		}
	}
	stage("recovery", notes, recovery)
	if recovery == "warn" && out.Verdict == Ready {
		out.Verdict = Degraded
	}
	return out, nil
}

type Engine struct {
	Health       *healthlayer.Collector
	Cache        *Cache
	Effect       func(string) string
	Available    func(context.Context, string) (registered, authorized bool)
	CompactError func(string) string
}

func healthAction(tool string, arguments map[string]any) *healthlayer.Action {
	raw, _ := json.Marshal(arguments)
	return &healthlayer.Action{Tool: tool, Args: raw, Approval: "none"}
}
