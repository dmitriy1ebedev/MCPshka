package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/healthlayer"
	"mcpshka/internal/jobs"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/tools/health"
	"strings"
)

var healthSample = health.Sample
var healthOutputSchema = health.OutputSchema

func newHealthCollector(m *jobs.Manager, control *controlplane.Service, units *systemdsvc.Service, docker *dockerops.Service, lane *laneprovider.Service, registry *toolRegistry, heal ...*selfheal.Service) *healthlayer.Collector {
	extra := blockerHealthProbes(brokerclient.Run)
	extra = append(extra, missionHealthProbe(newMissionReader(m)))
	extra = append(extra, providerHealthProbes(registry, m, brokerclient.Run)...)
	return health.NewCollector(m, control, units, docker, lane, extra, heal...)
}
func registerHealthTools(r *toolRegistry, collector *healthlayer.Collector) {
	health.Register(r.install, collector)
}

func healthSnapshotCompat(ctx context.Context, m *jobs.Manager, c *healthlayer.Collector, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Args) != 1 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return jobs.Snapshot{}, errors.New("health-snapshot requires no arguments or overrides and a retry key")
	}
	if _, e := m.Policy().CheckWorkdir(in.Workdir); e != nil {
		return jobs.Snapshot{}, e
	}
	out, e := c.Read(ctx)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	return m.StoreReceipt("mcpshka", []string{"health-snapshot"}, in.Workdir, string(raw))
}
