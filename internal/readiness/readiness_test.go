package readiness

import (
	"encoding/json"
	"testing"
	"time"

	"mcpshka/internal/healthlayer"
)

func readinessReport(states map[string]string) *healthlayer.Report {
	r := &healthlayer.Report{CapturedAt: time.Now()}
	for name, state := range states {
		r.Items = append(r.Items, healthlayer.Item{Name: name, Sample: healthlayer.Sample{State: state}})
	}
	return r
}

func TestReadinessReadyNeedsEveryDependencyAndSelfTest(t *testing.T) {
	all := readinessReport(map[string]string{"broker": "ok", "critical_services": "ok", "containers": "ok"})
	if got := evaluateReadiness("docker_list", "read_only", all, nil); got.State != Ready || got.BackendHealthy != "true" {
		t.Fatalf("docker_list with healthy sources: %+v", got)
	}
	// docker_smoke declares a self-test: healthy sources alone are not proof.
	if got := evaluateReadiness("docker_smoke", "gated_mutation", all, nil); got.State != Preflight || got.BackendHealthy != "unknown" {
		t.Fatalf("docker_smoke without self-test: %+v", got)
	}
	test := &selfTestEntry{state: Ready, raw: json.RawMessage(`{"state":"ready"}`), at: time.Now()}
	got := evaluateReadiness("docker_smoke", "gated_mutation", all, test)
	if got.State != Ready || string(got.SelfTest) != `{"state":"ready"}` || got.Privilege != "root_broker" || len(got.Directories) == 0 {
		t.Fatalf("docker_smoke with passing self-test: %+v", got)
	}
	test.state = Blocked
	if got := evaluateReadiness("docker_smoke", "gated_mutation", all, test); got.State != Blocked || got.BackendHealthy != "false" {
		t.Fatalf("blocked self-test: %+v", got)
	}
}

func TestReadinessFailClosed(t *testing.T) {
	if got := evaluateReadiness("docker_list", "read_only", nil, nil); got.State != Preflight {
		t.Fatalf("no evidence must be preflight, got %s", got.State)
	}
	stale := readinessReport(map[string]string{"broker": "ok", "critical_services": "stale", "containers": "ok"})
	if got := evaluateReadiness("docker_list", "read_only", stale, nil); got.State != Preflight {
		t.Fatalf("stale required source must be preflight, got %s", got.State)
	}
	required := readinessReport(map[string]string{"broker": "degraded", "critical_services": "ok", "containers": "ok"})
	if got := evaluateReadiness("docker_list", "read_only", required, nil); got.State != Blocked || got.Reason == "" {
		t.Fatalf("degraded required source must block: %+v", got)
	}
	optional := readinessReport(map[string]string{"broker": "ok", "critical_services": "ok", "containers": "degraded"})
	if got := evaluateReadiness("docker_list", "read_only", optional, nil); got.State != Degraded {
		t.Fatalf("degraded optional source must degrade, got %s", got.State)
	}
	if got := evaluateReadiness("mcpshka_guide", "read_only", optional, nil); got.State != Registered || got.BackendHealthy != "unknown" {
		t.Fatalf("capability without dependencies stays registered: %+v", got)
	}
}

func TestContractPrefixOrder(t *testing.T) {
	smoke, _ := contractFor("docker_smoke_status")
	if smoke.selfTest != "docker_smoke" {
		t.Fatal("docker_smoke_status must use the docker_smoke contract, not the docker_ family")
	}
	list, _ := contractFor("docker_list")
	if list.selfTest != "" {
		t.Fatal("docker_list must not inherit the docker_smoke self-test")
	}
}
