package hostload

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type fakeHost struct {
	t   *testing.T
	dir string
	at  time.Time
}

func newFake(t *testing.T) *fakeHost {
	f := &fakeHost{t: t, dir: t.TempDir(), at: time.Unix(1000, 0)}
	oldRoot, oldNow, oldCPUs := procRoot, now, cpus
	procRoot, now, cpus = f.dir, func() time.Time { return f.at }, func() int { return 4 }
	mu.Lock()
	ewma, sampled, busy = 0, time.Time{}, false
	mu.Unlock()
	t.Cleanup(func() { procRoot, now, cpus = oldRoot, oldNow, oldCPUs })
	return f
}

func (f *fakeHost) set(load1 float64, running int) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "loadavg"), []byte(strconv.FormatFloat(load1, 'f', 2, 64)+" 1 1 1/1 1\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "stat"), []byte("cpu 1 2 3\nprocs_running "+strconv.Itoa(running+1)+"\nprocs_blocked 9\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// A disk-bound load1 of 12 with few runnable tasks is not CPU pressure.
func TestDiskWaitIsNotCPUBusy(t *testing.T) {
	f := newFake(t)
	f.set(10, 2)
	if r := Read(); r.Busy {
		t.Fatalf("disk-bound host marked busy: %s", r)
	}
}

func TestHysteresisAndHardLimit(t *testing.T) {
	f := newFake(t)
	f.set(8, 8)
	if r := Read(); !r.Busy {
		t.Fatalf("8 runnable on 4 CPU not busy: %s", r)
	}
	// 5.5 runnable = 1.375 per CPU: between Exit and Enter stays busy.
	f.set(6, 5)
	for range 20 {
		f.at = f.at.Add(5 * time.Second)
		Read()
	}
	f.set(6, 6)
	f.at = f.at.Add(5 * time.Second)
	if r := Read(); !r.Busy {
		t.Fatalf("left busy between thresholds: %s", r)
	}
	f.set(2, 2)
	for range 30 {
		f.at = f.at.Add(5 * time.Second)
		Read()
	}
	if r := Read(); r.Busy {
		t.Fatalf("calm host still busy: %s", r)
	}
	f.set(13, 0)
	if r := Read(); !r.Busy {
		t.Fatalf("load1 over hard limit not busy: %s", r)
	}
}

func TestShortSpikeIsSmoothed(t *testing.T) {
	f := newFake(t)
	f.set(2, 2)
	Read()
	f.set(2, 20)
	f.at = f.at.Add(5 * time.Second)
	if r := Read(); r.Busy {
		t.Fatalf("5 s spike made the host busy: %s", r)
	}
}
