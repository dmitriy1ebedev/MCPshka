package homeguard

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MCP-159: H96 froze for 3.5 h in swap thrash; the kernel has no PSI and
// PSI misses zram freezes anyway, so the guard reads the counters
// earlyoom/Android use: MemAvailable, swap fill, and the per-second rate of
// swap traffic and major faults over the last sample interval.

// MemSample is one read of /proc/meminfo + /proc/vmstat.
type MemSample struct {
	At                      time.Time
	TotalKB, AvailKB        uint64
	SwapTotalKB, SwapFreeKB uint64
	SwapPages, MajFaults    uint64 // pswpin+pswpout, pgmajfault (cumulative)
}

// ProcRoot is swapped by tests.
var ProcRoot = "/proc"

func ReadMem(now time.Time) (MemSample, error) {
	s := MemSample{At: now}
	mem, err := readKV(filepath.Join(ProcRoot, "meminfo"))
	if err != nil {
		return s, err
	}
	vm, err := readKV(filepath.Join(ProcRoot, "vmstat"))
	if err != nil {
		return s, err
	}
	// A partial/truncated procfs snapshot must never be reported as a
	// healthy reading. Zero can be valid for swap, but missing cannot.
	for _, key := range []string{"MemTotal:", "MemAvailable:", "SwapTotal:", "SwapFree:"} {
		if _, ok := mem[key]; !ok {
			return s, fmt.Errorf("meminfo missing required counter %s", key)
		}
	}
	for _, key := range []string{"pswpin", "pswpout", "pgmajfault"} {
		if _, ok := vm[key]; !ok {
			return s, fmt.Errorf("vmstat missing required counter %s", key)
		}
	}
	s.TotalKB, s.AvailKB = mem["MemTotal:"], mem["MemAvailable:"]
	s.SwapTotalKB, s.SwapFreeKB = mem["SwapTotal:"], mem["SwapFree:"]
	if s.TotalKB == 0 || s.AvailKB > s.TotalKB || s.SwapFreeKB > s.SwapTotalKB {
		return s, fmt.Errorf("invalid procfs memory/swap totals")
	}
	if vm["pswpin"] > ^uint64(0)-vm["pswpout"] {
		return s, fmt.Errorf("vmstat swap counters overflow")
	}
	s.SwapPages, s.MajFaults = vm["pswpin"]+vm["pswpout"], vm["pgmajfault"]
	return s, nil
}

func readKV(path string) (map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 {
			if v, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				out[f[0]] = v
			}
		}
	}
	return out, sc.Err()
}

// Thresholds: 4 KiB pages, so 1000 pages/s is 4 MB/s of swap traffic.
const (
	thrashSwapPagesPerSec = 1000
	thrashMajFaultsPerSec = 500
	thrashAvailPct        = 10
	lowAvailPct           = 6 // earlyoom on H96 runs -m 6 -s 15
	lowSwapFreePct        = 15
)

// Thrash names memory trouble ("" = fine). prev may be zero: then only the
// level check runs.
func Thrash(prev, cur MemSample) string {
	avail := cur.AvailKB * 100 / cur.TotalKB
	swapFree := uint64(100)
	if cur.SwapTotalKB > 0 {
		swapFree = cur.SwapFreeKB * 100 / cur.SwapTotalKB
	}
	if avail < lowAvailPct && swapFree < lowSwapFreePct {
		return fmt.Sprintf("memory_low: MemAvailable %d%%, swap free %d%%", avail, swapFree)
	}
	dt := cur.At.Sub(prev.At).Seconds()
	if prev.At.IsZero() || dt < 1 || cur.SwapPages < prev.SwapPages || cur.MajFaults < prev.MajFaults {
		return ""
	}
	swapRate := float64(cur.SwapPages-prev.SwapPages) / dt
	faultRate := float64(cur.MajFaults-prev.MajFaults) / dt
	if avail < thrashAvailPct && (swapRate > thrashSwapPagesPerSec || faultRate > thrashMajFaultsPerSec) {
		return fmt.Sprintf("memory_thrash: swap %.0f pages/s, major faults %.0f/s, MemAvailable %d%%", swapRate, faultRate, avail)
	}
	return ""
}
