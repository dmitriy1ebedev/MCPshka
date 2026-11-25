package verify

import (
	"context"
	"os"
	"strings"

	"mcpshka/internal/homeguard"
)

// homeOffFile is the owner's off switch; tests point it away from the host's
// real file (the 0.13.5 rollback left /etc/mcpshka/home-priority-off on H96).
var homeOffFile = homeguard.OffFile

// HeavyPlacement reports where heavy jobs start in this server process (set
// by the server runtime; nil skips the check). 24.11: the delegation drop-in
// was installed but the service never restarted, heavy tests ran in the
// server's own cgroup, and home_priority still read OK from unit weights.
var HeavyPlacement func() (bool, string)

// Home and heavy-slice drift fails project verification.
func (s *Service) addHomePriority(ctx context.Context, result *Result) {
	_, err := os.Stat(homeOffFile)
	names, discoverErr := homeguard.HomePriorityUnits(ctx, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return s.run(ctx, strings.TrimPrefix(name, "/usr/bin/"), args...)
	})
	if discoverErr != nil {
		result.add("home_priority", false, bounded(discoverErr.Error(), 300))
		return
	}
	ok, detail := homeguard.Drift(func(unit string) (string, error) {
		out, err := s.run(ctx, "systemctl", "show", "--property=CPUWeight,IOWeight,CPUQuotaPerSecUSec", unit)
		return string(out), err
	}, err == nil, names...)
	result.add("home_priority", ok, bounded(detail, 300))
	if HeavyPlacement != nil {
		ok, detail := HeavyPlacement()
		result.add("heavy_jobs_isolated", ok, bounded(detail, 300))
	}
}
