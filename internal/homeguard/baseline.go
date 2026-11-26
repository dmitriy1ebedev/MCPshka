package homeguard

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ROADMAP P20: anomaly detection without models. Every minute the guard
// keeps one point of the host's own numbers for 26 hours; host_triage
// scores the last minutes against the day's median/MAD (moving window) and
// against the same hour yesterday (seasonal: nightly backups are no news).

// BaselineFile is swapped by tests.
var BaselineFile = "/run/mcpshka-broker/baseline.json"

const (
	baselineKeep = 26 * 60 // points, one a minute
	baselineMin  = 120     // two hours before anything is scored
	recentN      = 3       // the last minutes, median, so one spike is not news
	zLimit       = 5.0
)

// Point is one minute of the host.
type Point struct {
	At       time.Time `json:"t"`
	Load1    float64   `json:"l"`
	AvailPct float64   `json:"a"`
	SwapPS   float64   `json:"s"` // swap pages/s since the previous sample
	FaultsPS float64   `json:"f"` // major faults/s
}

// metric: how to read it, whether up is bad, and the smallest change that
// matters whatever the statistics say.
var metrics = []struct {
	name  string
	get   func(Point) float64
	up    bool
	floor float64
}{
	{"load1", func(p Point) float64 { return p.Load1 }, true, 2},
	{"MemAvailable %", func(p Point) float64 { return p.AvailPct }, false, 10},
	{"swap pages/s", func(p Point) float64 { return p.SwapPS }, true, 50},
	{"major faults/s", func(p Point) float64 { return p.FaultsPS }, true, 100},
}

func pointOf(prev, cur MemSample) Point {
	p := Point{At: cur.At, AvailPct: float64(cur.AvailKB) * 100 / float64(max(cur.TotalKB, 1))}
	if raw, err := os.ReadFile(filepath.Join(ProcRoot, "loadavg")); err == nil {
		if f := strings.Fields(string(raw)); len(f) > 0 {
			p.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if dt := cur.At.Sub(prev.At).Seconds(); !prev.At.IsZero() && dt >= 1 && cur.SwapPages >= prev.SwapPages && cur.MajFaults >= prev.MajFaults {
		p.SwapPS = float64(cur.SwapPages-prev.SwapPages) / dt
		p.FaultsPS = float64(cur.MajFaults-prev.MajFaults) / dt
	}
	return p
}

// ReadBaseline returns the kept points, oldest first (none on any error).
func ReadBaseline() []Point {
	raw, err := os.ReadFile(BaselineFile)
	var pts []Point
	if err != nil || json.Unmarshal(raw, &pts) != nil {
		return nil
	}
	return pts
}

func appendBaseline(p Point) error {
	pts := append(ReadBaseline(), p)
	if len(pts) > baselineKeep {
		pts = pts[len(pts)-baselineKeep:]
	}
	raw, _ := json.Marshal(pts)
	tmp := BaselineFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, BaselineFile)
}

// Anomalies names each metric whose last minutes stand out from both the
// last 24 hours and the same hour yesterday.
func Anomalies(pts []Point) []string {
	if len(pts) < baselineMin+recentN {
		return nil
	}
	now := pts[len(pts)-1].At
	recent, past := pts[len(pts)-recentN:], pts[:len(pts)-recentN]
	var out []string
	for _, m := range metrics {
		cur := median(values(recent, m.get))
		day := values(since(past, now.Add(-24*time.Hour)), m.get)
		med := median(day)
		z := robustZ(cur, med, day)
		if !m.up {
			z = -z
		}
		if z < zLimit || math.Abs(cur-med) < m.floor {
			continue
		}
		// Seasonal: the same hour yesterday looked like this — routine.
		if season := values(window(past, now.Add(-24*time.Hour), 30*time.Minute), m.get); len(season) >= 10 {
			if s := median(season); (m.up && cur <= s+m.floor) || (!m.up && cur >= s-m.floor) {
				continue
			}
		}
		out = append(out, fmt.Sprintf("%s %.1f vs %.1f usual (robust z %.0f)", m.name, cur, med, z))
	}
	return out
}

func values(pts []Point, get func(Point) float64) []float64 {
	out := make([]float64, len(pts))
	for i, p := range pts {
		out[i] = get(p)
	}
	return out
}

func since(pts []Point, from time.Time) []Point {
	i := sort.Search(len(pts), func(i int) bool { return !pts[i].At.Before(from) })
	return pts[i:]
}

func window(pts []Point, at time.Time, half time.Duration) []Point {
	var out []Point
	for _, p := range pts {
		if d := p.At.Sub(at); d >= -half && d <= half {
			out = append(out, p)
		}
	}
	return out
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

// robustZ is (x-median)/(1.4826·MAD); a flat history gets a small MAD floor
// so a quiet host does not alarm on noise.
func robustZ(x, med float64, v []float64) float64 {
	dev := make([]float64, len(v))
	for i, y := range v {
		dev[i] = math.Abs(y - med)
	}
	mad := 1.4826 * median(dev)
	return (x - med) / math.Max(mad, 0.05*math.Max(math.Abs(med), 1))
}
