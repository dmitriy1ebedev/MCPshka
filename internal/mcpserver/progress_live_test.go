package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/mission"
	"mcpshka/internal/releasequeue"
	missiontools "mcpshka/internal/tools/mission"
	"strings"
	"testing"
	"time"
)

func TestReleaseAndMissionProgressThroughTools(t *testing.T) {
	m, _, in := yieldFixture(t)
	now := time.Now().UTC()
	q, err := releasequeue.Mutate(m.Database(), authn.Identity{ID: "owner"}, releasequeue.Input{Version: "0.12.9", Branch: "feature", Commit: strings.Repeat("a", 40), StoreID: strings.Repeat("b", 32)}, now)
	if err != nil {
		t.Fatal(err)
	}
	items, err := releasequeue.Load(context.Background(), m.Database())
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	items[0].Stage = "BUILD"
	items[0].TestPermit = "ci-pass-permit"
	raw, _ := json.Marshal(items[0])
	if err = m.Database().Put(releasequeue.Prefix+q.Version+".json", raw); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldUnit, oldRoot := releaseInstalledVersion, releaseUnitOutput, releaseRootStatus
	t.Cleanup(func() { releaseInstalledVersion, releaseUnitOutput, releaseRootStatus = oldVersion, oldUnit, oldRoot })
	releaseRootStatus = func(context.Context, string) ([]byte, error) { return []byte(`{"state":"NOT_STARTED"}`), nil }
	releaseInstalledVersion = func(context.Context) string { return "0.12.8" }
	releaseUnitOutput = func(context.Context, ...string) ([]byte, error) { return []byte("inactive\nsuccess"), nil }
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerReleaseApplyTools(r, m.Database())
	reader := missiontools.New(missiontools.Deps{Manager: m, Tasks: t.TempDir(), Runs: t.TempDir(), Projects: in.Path, Unit: func(context.Context, string) string { return "inactive" }})
	registerMissionTools(r, reader)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"release_apply_status", "mission_status"}})
	v, err := r.readers["release_apply_status"](ctx, map[string]any{"version": q.Version})
	if err != nil {
		t.Fatal(err)
	}
	got := v.(ReleaseApplyStatusOutput)
	if got.PlannedStep != "release_build_prepare" || got.ObservedState != "NOT_STARTED" || got.CheckpointRef == "" || got.Freshness.IsZero() || got.SafeParallelWork == "" || got.Stages.Accepted || got.Stages.ProductionVerified || !got.Stages.Integrated {
		t.Fatal(got)
	}
	v, err = r.readers["mission_status"](ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rep := v.(mission.Report)
	if rep.PlannedStep == "" || rep.ObservedState == "" || rep.Freshness.IsZero() || rep.SafeParallelWork == "" {
		t.Fatal(rep)
	}
}
