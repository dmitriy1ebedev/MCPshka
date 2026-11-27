package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/hosttriage"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/statestore"
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
func TestSelfHealStatusReadOnlyProtocolAndPaging(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	docker := &statusOnlyDocker{}
	s := selfheal.New(db, docker, nil, selfheal.Config{Enabled: true, UnhealthyMinutes: 5, VerifyMinutes: 3, MaxRepairs: 2})
	st := selfheal.State{Containers: map[string]selfheal.Container{}, Incidents: []selfheal.Incident{{At: time.Now().UTC(), Container: "zigbee2mqtt", Fingerprint: "failure", Result: "escalated: hourly repair limit"}}}
	for _, name := range []string{"a", "b", "c", "d", "e", "zigbee2mqtt"} {
		st.Containers[name] = selfheal.Container{Fingerprint: "failure", Phase: "escalated: hourly repair limit", History: []selfheal.Repair{}}
	}
	raw, _ := json.Marshal(st)
	if _, e = db.PutIfAbsent("selfheal/state.json", raw); e != nil {
		t.Fatal(e)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "self-heal-test", Version: "1"}, nil)
	r := &toolRegistry{server: srv, tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerSelfHealTool(r, s)
	r.validate()
	if !r.tools["self_heal_status"] || r.readers["self_heal_status"] == nil {
		t.Fatal("missing read-only contract")
	}
	if _, ok := mutationContracts["self_heal_status"]; ok {
		t.Fatal("status gained mutation contract")
	}
	reader := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"self_heal_status"}})
	v, e := r.readers["self_heal_status"](reader, map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	page := v.(SelfHealStatusResult)
	if len(page.Containers) != 4 || !page.Truncated || page.NextCursor != 4 || !page.Active || page.MaxRepairs != 2 {
		t.Fatal(page)
	}
	next, e := selfHealPage(context.Background(), s, page.NextCursor)
	if e != nil || len(next.Containers) != 2 || next.Truncated {
		t.Fatal(next, e)
	}
	if _, e = selfHealPage(context.Background(), s, -1); e == nil {
		t.Fatal("accepted negative cursor")
	}
	summary, e := hosttriage.Summary(context.Background(), s)
	if e != nil || summary.Last == nil || summary.Escalated != 6 {
		t.Fatal(summary, e)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, e := srv.Connect(reader, serverTransport, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), clientTransport, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	out, e := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "self_heal_status", Arguments: map[string]any{}})
	if e != nil || out.IsError {
		t.Fatalf("native schema rejected: %+v %v", out, e)
	}
	stored, e := db.Get("selfheal/state.json")
	if e != nil || string(stored) != string(raw) || docker.calls != 0 {
		t.Fatal("status mutated or polled Docker")
	}
}
func TestSelfHealBadConfigurationRejectsStartup(t *testing.T) {
	t.Setenv("MCPSHKA_SELF_HEAL_MAX_REPAIRS", "0")
	manager, _, _, stop, e := newServicesFromEnv()
	if e == nil || manager != nil || stop != nil {
		t.Fatal("invalid self-heal config started services")
	}
}
func TestSelfHealEmptyStatusDoesNotCreateState(t *testing.T) {
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	s := selfheal.New(db, &statusOnlyDocker{}, nil, selfheal.Config{})
	out, e := selfHealPage(context.Background(), s, 0)
	if e != nil || out.Active {
		t.Fatal(out, e)
	}
	if _, e = db.Get("selfheal/state.json"); !os.IsNotExist(e) {
		t.Fatal("status created state")
	}
}
