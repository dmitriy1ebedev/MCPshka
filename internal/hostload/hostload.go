// Package hostload tells whether the host has CPU room for heavy work.
//
// load1 also counts tasks blocked on disk, so a busy disk looked like a busy
// CPU and a single threshold flapped around it. The signal here is the number
// of runnable tasks (procs_running) smoothed over about 30 s, with hysteresis:
// the host becomes busy above Enter per CPU and calm again only below Exit.
// load1 above Hard per CPU is busy regardless, as a safety net.
package hostload

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	Enter = 1.5
	Exit  = 1.2
	Hard  = 3.0
	tau   = 30 * time.Second
)

// Reading is one observation of the host.
type Reading struct {
	PerCPU float64 // smoothed runnable tasks per CPU
	Load1  float64
	CPUs   int
	Busy   bool
}

func (r Reading) String() string {
	return fmt.Sprintf("runnable %.2f per CPU (busy above %.1f, calm below %.1f), load1 %.2f on %d CPU", r.PerCPU, Enter, Exit, r.Load1, r.CPUs)
}

var (
	mu       sync.Mutex
	ewma     float64
	sampled  time.Time
	busy     bool
	procRoot = "/proc"
	now      = time.Now
	cpus     = runtime.NumCPU
)

// Read samples the host and updates the smoothed signal.
func Read() Reading {
	load1 := readLoad1()
	running := readRunning()
	mu.Lock()
	defer mu.Unlock()
	t := now()
	switch {
	case running < 0:
		ewma = load1
	case sampled.IsZero():
		ewma = math.Min(running, load1)
	default:
		alpha := 1 - math.Exp(-float64(t.Sub(sampled))/float64(tau))
		ewma += alpha * (running - ewma)
	}
	sampled = t
	n := max(cpus(), 1)
	per := ewma / float64(n)
	if busy {
		busy = per > Exit
	} else {
		busy = per > Enter
	}
	if load1/float64(n) > Hard {
		busy = true
	}
	return Reading{PerCPU: per, Load1: load1, CPUs: n, Busy: busy}
}

func readLoad1() float64 {
	raw, err := os.ReadFile(filepath.Join(procRoot, "loadavg"))
	if err != nil {
		return 0
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// readRunning returns runnable tasks other than the reader, or -1.
func readRunning() float64 {
	raw, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "procs_running "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return -1
			}
			return float64(max(n-1, 0))
		}
	}
	return -1
}
