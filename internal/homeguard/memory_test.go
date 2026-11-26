package homeguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCP159ReadMemRejectsIncompleteOrInconsistentProcfs(t *testing.T) {
	dir := t.TempDir()
	old := ProcRoot
	ProcRoot = dir
	defer func() { ProcRoot = old }()
	mem := filepath.Join(dir, "meminfo")
	vm := filepath.Join(dir, "vmstat")
	sample := func(meminfo, vmstat string) error {
		if err := os.WriteFile(mem, []byte(meminfo), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vm, []byte(vmstat), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadMem(time.Now())
		return err
	}
	good := "MemTotal: 4000000 kB\nMemAvailable: 1000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"
	goodVM := "pswpin 0\npswpout 0\npgmajfault 5\n"
	if err := sample(good, goodVM); err != nil {
		t.Fatalf("valid no-swap host refused: %v", err)
	}
	for _, tt := range []struct{ name, mem, vm string }{
		{"no available", "MemTotal: 4000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n", goodVM},
		{"no swapfree", "MemTotal: 4000000 kB\nMemAvailable: 1000000 kB\nSwapTotal: 0 kB\n", goodVM},
		{"inconsistent mem", "MemTotal: 4000000 kB\nMemAvailable: 5000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n", goodVM},
		{"inconsistent swap", "MemTotal: 4000000 kB\nMemAvailable: 1000000 kB\nSwapTotal: 100 kB\nSwapFree: 200 kB\n", goodVM},
		{"no page faults", good, "pswpin 0\npswpout 0\n"},
		{"swap overflow", good, "pswpin 18446744073709551615\npswpout 1\npgmajfault 1\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := sample(tt.mem, tt.vm); err == nil {
				t.Fatal("broken procfs sample reported as healthy")
			}
		})
	}
}

func TestThrashLevelsAndRates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	base := MemSample{At: t0, TotalKB: 4000000, AvailKB: 300000, SwapTotalKB: 2000000, SwapFreeKB: 800000}
	if why := Thrash(MemSample{}, base); why != "" {
		t.Fatalf("calm host flagged: %s", why)
	}
	hot := base
	hot.At, hot.SwapPages = t0.Add(time.Minute), 120000 // 2000 pages/s, avail 7.5%
	if why := Thrash(base, hot); !strings.HasPrefix(why, "memory_thrash") {
		t.Fatalf("thrash missed: %q", why)
	}
	roomy := hot
	roomy.AvailKB = 2000000
	if why := Thrash(base, roomy); why != "" {
		t.Fatalf("swap traffic with free RAM is not thrash: %s", why)
	}
	low := base
	low.AvailKB, low.SwapFreeKB = 200000, 100000
	if why := Thrash(MemSample{}, low); !strings.HasPrefix(why, "memory_low") {
		t.Fatalf("low memory missed: %q", why)
	}
}

func TestReadMem(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "meminfo"), []byte("MemTotal: 3853884 kB\nMemAvailable: 1183996 kB\nSwapTotal: 1926940 kB\nSwapFree: 829304 kB\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "vmstat"), []byte("pswpin 10\npswpout 5\npgmajfault 7\n"), 0o644)
	old := ProcRoot
	ProcRoot = dir
	defer func() { ProcRoot = old }()
	s, err := ReadMem(time.Now())
	if err != nil || s.TotalKB != 3853884 || s.SwapPages != 15 || s.MajFaults != 7 {
		t.Fatalf("%+v %v", s, err)
	}
}

// A day of a quiet host, then a load and memory jump: both are anomalies;
// the same jump at the same hour yesterday is routine.
func TestAnomaliesAgainstDayAndSeason(t *testing.T) {
	start := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	var pts []Point
	for i := 0; i < 25*60; i++ {
		pts = append(pts, Point{At: start.Add(time.Duration(i) * time.Minute), Load1: 1 + float64(i%5)/10, AvailPct: 45})
	}
	if a := Anomalies(pts); len(a) != 0 {
		t.Fatalf("quiet host: %v", a)
	}
	end := pts[len(pts)-1].At
	for i := 1; i <= 3; i++ {
		pts = append(pts, Point{At: end.Add(time.Duration(i) * time.Minute), Load1: 7, AvailPct: 12})
	}
	a := Anomalies(pts)
	if len(a) != 2 || !strings.HasPrefix(a[0], "load1 7.0") || !strings.HasPrefix(a[1], "MemAvailable % 12.0") {
		t.Fatalf("jump: %v", a)
	}
	// Yesterday at this hour the host looked the same: no news.
	now := pts[len(pts)-1].At
	for i := range pts {
		if d := pts[i].At.Sub(now.Add(-24 * time.Hour)); d > -20*time.Minute && d < 20*time.Minute {
			pts[i].Load1, pts[i].AvailPct = 7, 12
		}
	}
	if a := Anomalies(pts); len(a) != 0 {
		t.Fatalf("seasonal: %v", a)
	}
}
