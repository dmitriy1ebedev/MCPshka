package homeguard

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/heavyqueue"
)

// weight is one unit's share under contention. The persistent heavy slice
// caps aggregate work; per-unit limits still bound each worker.
type weight struct{ cpu, io, quota string } // quota: "300%" or none

var fixed = map[string]weight{
	"user.slice": {"30", "30", "300%"},
	// P5.3: all heavy work together (sealed tests, release builds, agents)
	// never takes the fourth core from the home; weights matter only under
	// contention, so an idle box still builds on everything.
	heavyqueue.HeavySlice: {"20", "10", "300%"},
}

var homeWeight = weight{"1000", "1000", ""}

func (w weight) set() []string {
	p := []string{"CPUWeight=" + w.cpu, "IOWeight=" + w.io}
	if w.quota != "" {
		p = append(p, "CPUQuota="+w.quota)
	}
	return p
}

// reset is the boot value: runtime properties exist only because Weights
// set them, so an empty assignment is the exact backup.
func (w weight) reset() []string {
	p := []string{"CPUWeight=", "IOWeight="}
	if w.quota != "" {
		p = append(p, "CPUQuota=")
	}
	return p
}

var containerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// units lists the fixed units plus the running home containers' scopes.
func units(ctx context.Context, run Exec) map[string]weight {
	out := map[string]weight{}
	for u, w := range fixed {
		out[u] = w
	}
	for _, name := range homeContainers {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		id, err := run(c, "/usr/bin/docker", "inspect", "-f", idTemplate, name)
		cancel()
		if id := strings.TrimSpace(string(id)); err == nil && containerID.MatchString(id) {
			out["docker-"+id+".scope"] = homeWeight
		}
	}
	return out
}

func apply(ctx context.Context, run Exec, props func(weight) []string) error {
	names, err := HomeUnits(ctx, run)
	if err != nil {
		return err
	}
	all := units(ctx, run)
	for _, unit := range names {
		all[unit] = homeWeight
	}
	var first error
	for unit, w := range all {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		out, err := run(c, "/usr/bin/systemctl", append([]string{"set-property", "--runtime", "--", unit}, props(w)...)...)
		cancel()
		if err != nil && first == nil {
			first = &weightError{unit, strings.TrimSpace(string(out)), err}
		}
	}
	return first
}

// Weights gives the home the CPU and disk first under contention.
func Weights(ctx context.Context, run Exec) error { return apply(ctx, run, weight.set) }

// Reset is the rollback: every unit Weights touches gets its boot value.
func Reset(ctx context.Context, run Exec) error { return apply(ctx, run, weight.reset) }

type weightError struct {
	unit, out string
	err       error
}

func (e *weightError) Error() string {
	return "home weights " + e.unit + ": " + e.err.Error() + " " + e.out
}

// Drift compares the fixed units' properties (`systemctl show` text per
// unit) with what the guard sets, or with boot values while it is off.
// The detail names every mismatch; project_verify reports it.
func Drift(show func(unit string) (string, error), off bool, home ...string) (bool, string) {
	if off {
		return true, "home priority off: original weights are owner policy"
	}
	all := make(map[string]weight, len(fixed)+len(home)+1)
	for u, w := range fixed {
		all[u] = w
	}
	for _, u := range home {
		all[u] = homeWeight
	}
	if !off {
		all[heavyqueue.HeavySlice] = weight{cpu: "20", io: "10", quota: "300%"}
	}
	names := make([]string, 0, len(all))
	for u := range all {
		names = append(names, u)
	}
	sort.Strings(names)
	var bad []string
	for _, u := range names {
		raw, err := show(u)
		if err != nil {
			bad = append(bad, u+": unreadable")
			continue
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				got[k] = v
			}
		}
		want := expected(all[u], off)
		keys := []string{"CPUWeight", "IOWeight"}
		if all[u].quota != "" {
			keys = append(keys, "CPUQuotaPerSecUSec")
		}
		for _, k := range keys {
			if got[k] != want[k] {
				bad = append(bad, fmt.Sprintf("%s %s=%s (want %s)", u, k, got[k], want[k]))
			}
		}
	}
	state := "on"
	if off {
		state = "off"
	}
	if len(bad) > 0 {
		return false, "home priority " + state + ", drift: " + strings.Join(bad, "; ")
	}
	return true, "home priority " + state + ": weights as set"
}

func expected(w weight, off bool) map[string]string {
	if off {
		return map[string]string{"CPUWeight": "[not set]", "IOWeight": "[not set]", "CPUQuotaPerSecUSec": "infinity"}
	}
	quota := "infinity"
	if pct, err := strconv.Atoi(strings.TrimSuffix(w.quota, "%")); err == nil && w.quota != "" {
		quota = strconv.Itoa(pct/100) + "s"
	}
	return map[string]string{"CPUWeight": w.cpu, "IOWeight": w.io, "CPUQuotaPerSecUSec": quota}
}
