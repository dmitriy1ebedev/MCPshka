package budget

import (
	"strings"
	"testing"
	"time"
)

func mins(v ...int) []time.Duration {
	var out []time.Duration
	for _, x := range v {
		out = append(out, time.Duration(x)*time.Minute)
	}
	return out
}

func TestDecideRules(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		wall time.Duration
		src  string
	}{
		{"default", Input{Default: 30 * time.Minute}, 30 * time.Minute, "default"},
		{"history", Input{Default: 30 * time.Minute, History: mins(20, 22, 25)}, 52 * time.Minute, "history"},
		{"request wins when enough", Input{Requested: 2 * time.Hour, History: mins(20)}, 2 * time.Hour, "requested"},
		{"too short request raised", Input{Requested: 30 * time.Minute, History: mins(28, 31)}, 64 * time.Minute, "raised"},
		{"load stretches", Input{Default: 30 * time.Minute, Load: 8, CPUs: 4}, time.Hour, "default"},
		{"clamped", Input{Requested: 9 * time.Hour}, Ceiling, "clamped"},
		{"floor", Input{History: []time.Duration{5 * time.Second}}, Floor + 0, "history"},
	}
	for _, c := range cases {
		d := Decide(c.in)
		if c.name == "floor" {
			c.wall = 2*time.Minute + 10*time.Second
		}
		if d.Wall != c.wall || d.Source != c.src {
			t.Fatalf("%s: %s %s", c.name, d.Wall, d.Source)
		}
		if !strings.Contains(d.String(), "бюджет") {
			t.Fatal(d.String())
		}
	}
}

func TestIdleWindow(t *testing.T) {
	if d := Decide(Input{Default: 5 * time.Minute}); d.Idle != 0 {
		t.Fatalf("short job got a stall window %s", d.Idle)
	}
	if d := Decide(Input{Requested: 90 * time.Minute}); d.Idle != 30*time.Minute {
		t.Fatalf("idle %s", d.Idle)
	}
	if d := Decide(Input{Requested: 6 * time.Hour}); d.Idle != MaxIdle {
		t.Fatalf("idle %s", d.Idle)
	}
}

func TestExtensionStopsAtCeiling(t *testing.T) {
	start := time.Unix(0, 0)
	if e := Extension(start, start.Add(time.Hour), start.Add(time.Hour)); e != 30*time.Minute {
		t.Fatal(e)
	}
	if e := Extension(start, start.Add(5*time.Hour+50*time.Minute), start.Add(5*time.Hour+50*time.Minute)); e != 10*time.Minute {
		t.Fatal(e)
	}
	if e := Extension(start, start.Add(Ceiling), start.Add(Ceiling)); e != 0 {
		t.Fatal(e)
	}
}

func TestP95(t *testing.T) {
	if P95(nil) != 0 || P95(mins(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 100)) != 19*time.Minute {
		t.Fatal("p95")
	}
}

func TestNoExtensionForShortJobs(t *testing.T) {
	start := time.Unix(0, 0)
	if e := Extension(start, start.Add(time.Minute), start.Add(time.Minute)); e != 0 {
		t.Fatal(e)
	}
}

func TestPhaseStretchesWithinMax(t *testing.T) {
	def := 2 * time.Minute
	if got := Phase(def); got < def || got > PhaseMax(def) {
		t.Fatalf("Phase(%s) = %s, want within [%s, %s]", def, got, def, PhaseMax(def))
	}
}
