package hosttriage

import (
	"context"
	"errors"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/statestore"
	"path/filepath"
	"testing"
)

type statusOnlyDocker struct{ calls int }

func (d *statusOnlyDocker) RestartContainers() []string { return []string{"zigbee2mqtt"} }
func (d *statusOnlyDocker) Status(context.Context, string) (dockerops.Status, error) {
	d.calls++
	return dockerops.Status{}, errors.New("must not poll Docker")
}
func (d *statusOnlyDocker) Inspect(context.Context, string) (dockerops.Inspect, error) {
	d.calls++
	return dockerops.Inspect{}, errors.New("must not poll Docker")
}
func (d *statusOnlyDocker) Restart(context.Context, string) (dockerops.Status, error) {
	d.calls++
	return dockerops.Status{}, errors.New("must not repair")
}
func TestSelfHealTriageEscalationStopsRestartRecommendation(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"containers":{"zigbee2mqtt":{"phase":"escalated: recovery not verified","history":[]}},"incidents":[{"container":"zigbee2mqtt","fingerprint":"failure","result":"escalated"}]}`)
	if _, e = db.PutIfAbsent("selfheal/state.json", raw); e != nil {
		t.Fatal(e)
	}
	s := selfheal.New(db, &statusOnlyDocker{}, nil, selfheal.Config{Enabled: true})
	out := HostTriageResult{Problems: []TriageProblem{{Kind: "container", Subject: "zigbee2mqtt", Action: &TriageAction{Tool: "docker_restart"}}}}
	attachSelfHealTriage(context.Background(), &out, s)
	if out.LastSelfHeal == nil || out.Problems[0].Action.Tool != "self_heal_status" || len(out.Problems) != 2 {
		t.Fatal(out)
	}
}

func TestMDNSFailureBecomesSentinelFinding(t *testing.T) {
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"containers":{"go2rtc":{"failure":"go2rtc LAN mDNS socket missing","fingerprint":"failure","phase":"escalated: recovery not verified","history":[]}},"incidents":[]}`)
	if _, err := db.PutIfAbsent("selfheal/state.json", raw); err != nil {
		t.Fatal(err)
	}
	docker := &statusOnlyDocker{}
	s := selfheal.New(db, docker, nil, selfheal.Config{Enabled: true})
	out := HostTriageResult{}
	attachSelfHealTriage(context.Background(), &out, s)
	findings := Findings(out.Problems)
	found := false
	for _, f := range findings {
		if f.Kind == "container-mdns" && f.Subject == "go2rtc" && f.Severity == "critical" {
			found = true
		}
	}
	if !found || docker.calls != 0 {
		t.Fatal("missing read-only issue sentinel finding", findings)
	}
}
