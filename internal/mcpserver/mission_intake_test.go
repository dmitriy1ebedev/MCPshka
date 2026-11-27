package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/gitops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/mission"
	"mcpshka/internal/projectprofile"
)

func TestMissionIntakeMCPRequiresWorkflowAuthorityAndReconcilesOriginal(t *testing.T) {
	m, _, in := yieldFixture(t)
	t.Cleanup(func() { m.Close(time.Second) })
	db := m.Database()
	pin := projectprofile.PipelinePin{ID: "mcp-intake", Path: in.Path, Pipeline: "release", RoadmapSHA: strings.Repeat("a", 64), Scope: "P1", Revision: in.ExpectedRevision, Fingerprint: strings.Repeat("b", 64)}
	wire := map[string]any{"id": pin.ID, "root": pin.Path, "scope": pin.Scope, "pipeline": pin.Pipeline, "revision": pin.Revision, "roadmap_sha256": pin.RoadmapSHA, "fingerprint": pin.Fingerprint, "state": "pending", "index": 0}
	c := mission.NewController(db, gitops.New(m.Policy()), func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
		switch name {
		case "capabilities_bootstrap":
			return map[string]any{"revision": strings.Repeat("a", 64)}, nil
		case "tool_resolve":
			return map[string]any{"code": "OK", "revision": strings.Repeat("a", 64), "candidates": []any{map[string]any{"name": args["goal"], "permission_granted": true, "input_schema": map[string]any{"type": "object"}}}}, nil
		case "capability_preflight":
			return map[string]any{"verdict": "ready"}, nil
		case "workflow_plan":
			return object(pin)
		case "workflow_status":
			if _, err := db.Get("fixture/child"); err != nil {
				return nil, err
			}
			return wire, nil
		case "acceptance_status":
			return map[string]any{"done": false}, nil
		}
		t.Fatal("unexpected tool", name)
		return nil, nil
	})
	effects, calls := 0, 0
	c.WorkflowDispatch = func(got projectprofile.PipelinePin, task string, deadline *time.Time, raw []byte) error {
		calls++
		if got != pin {
			t.Fatal("dispatch widened preview")
		}
		child, _ := json.Marshal(wire)
		ok, err := db.PutAllIfAbsent(map[string][]byte{"missions/" + task + ".json": raw, "fixture/child": child, "fixture/claim": []byte(pin.ID)}, "missions/", 256)
		if err != nil || !ok {
			t.Fatalf("fixture intake: %v %v", ok, err)
		}
		effects++
		return errors.New("reply lost after durable intake")
	}
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "intake-test", Version: "1"}, nil), store: idempotency.New(t.TempDir()), readers: map[string]probeHandler{}, tools: map[string]bool{}}
	r.readers["mission_status"] = func(ctx context.Context, args map[string]any) (any, error) {
		id, _ := args["id"].(string)
		if id == "" {
			return map[string]any{}, nil
		}
		return c.Status(ctx, id)
	}
	registerMissionControllerTools(r, c, r.store)
	args, err := object(mission.StartInput{Plan: mission.Plan{Path: pin.Path, Goal: "exact pipeline", Mode: "CLOSE_STEP", Step: "P1", WorkflowPlan: &pin}, IdempotencyKey: "original-intake"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner", Role: "operator", Scopes: []string{"mission_start"}})
	if _, _, err := r.invokers["mission_start"](ctx, args); err == nil {
		t.Fatal("mission_start scope granted workflow execution")
	}
	if count, err := db.Count("missions/"); err != nil || count != 0 || calls != 0 {
		t.Fatal("denied intake had an effect", count, calls, err)
	}
	ctx = authn.WithIdentity(context.Background(), authn.Identity{ID: "owner", Role: "operator", Scopes: []string{"mission_start", "workflow_start"}})
	if _, _, err := r.invokers["mission_start"](ctx, args); err == nil {
		t.Fatal("fixture did not lose reply")
	}
	for range 2 {
		_, out, err := r.invokers["mission_start"](ctx, args)
		if err != nil {
			t.Fatal("original intake not reconciled", err)
		}
		s := out.(mission.Snapshot)
		if s.WorkflowID != pin.ID || s.State == "DONE" {
			t.Fatal("lost original identity or invented acceptance", s)
		}
	}
	if calls != 1 || effects != 1 {
		t.Fatal("dispatch replayed", calls, effects)
	}
	if _, err := db.Get("fixture/claim"); err != nil {
		t.Fatal("lost writer authority", err)
	}
}
