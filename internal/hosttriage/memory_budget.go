package hosttriage

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// MCP-159: on 09.11 the services together wanted more memory than H96 has
// (HA, torrserver, Claude, desktop-commander…: memory+swap by cgroup above
// 3.7 GB of RAM) and the box swapped itself into a 3.5 h freeze. Page cache
// is reclaimable and zswap compresses, so the demand counted here is what
// cannot be dropped: anonymous memory plus everything swapped out, per
// cgroup v2. Above RAM the host is oversubscribed before it thrashes —
// a warning with the heaviest units, while there is still time to act.

// cgroupRoot is swapped by tests.
var cgroupRoot = "/sys/fs/cgroup"

type cgroupDemand struct {
	name  string
	bytes uint64
}

// demand reads anon (memory.stat) + memory.swap.current of one cgroup.
func demand(dir string) (uint64, bool) {
	f, err := os.Open(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	var anon uint64
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), " "); ok && k == "anon" {
			anon, err = strconv.ParseUint(v, 10, 64)
			found = err == nil
			break
		}
	}
	if !found {
		return 0, false
	}
	swap := uint64(0)
	if raw, err := os.ReadFile(filepath.Join(dir, "memory.swap.current")); err == nil {
		swap, _ = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	}
	return anon + swap, true
}

// unitName shortens a cgroup to what the owner recognizes.
func unitName(name string) string {
	if strings.HasPrefix(name, "docker-") && strings.HasSuffix(name, ".scope") && len(name) > 26 {
		return "docker:" + name[7:19]
	}
	return name
}

// memoryBudget returns a warning while total demand exceeds RAM.
func memoryBudget(memTotalKB uint64) (TriageProblem, bool) {
	if memTotalKB == 0 {
		return TriageProblem{}, false
	}
	var total uint64
	var units []cgroupDemand
	tops, _ := filepath.Glob(filepath.Join(cgroupRoot, "*"))
	for _, top := range tops {
		d, ok := demand(top)
		if !ok {
			continue
		}
		total += d
		kids, _ := filepath.Glob(filepath.Join(top, "*"))
		for _, k := range kids {
			if kd, ok := demand(k); ok && kd > 0 {
				units = append(units, cgroupDemand{unitName(filepath.Base(k)), kd})
			}
		}
	}
	ram := memTotalKB * 1024
	if total <= ram {
		return TriageProblem{}, false
	}
	sort.Slice(units, func(i, j int) bool { return units[i].bytes > units[j].bytes })
	var top []string
	for i := 0; i < len(units) && i < 5; i++ {
		top = append(top, fmt.Sprintf("%s %d MB", units[i].name, units[i].bytes>>20))
	}
	return TriageProblem{Severity: "warning", Kind: "memory_budget", Subject: "memory",
		Evidence: fmt.Sprintf("services need %d MB (anonymous + swapped) on %d MB of RAM; heaviest: %s", total>>20, ram>>20, strings.Join(top, ", ")),
		Action:   &TriageAction{Tool: "host_diagnose", Args: map[string]any{"symptom": "oom"}, Approval: "none"},
		Note:     "oversubscribed before it thrashes: stop or limit the heaviest non-home unit; heavy jobs swap the rest out"}, true
}
