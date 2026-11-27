// Package selfprof answers «what burns the CPU» on a running server without a
// listener or a restart (MCP-273: no pprof, strace or perf on H96). SIGUSR1
// makes the process write a 30 s CPU profile, a heap profile and a goroutine
// dump into dir; only root (or the service user) can send the signal, and the
// files are 0600. The last three runs are kept. Read with go tool pprof.
package selfprof

import (
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// Window is the CPU sampling time per signal.
var Window = 30 * time.Second

const keep = 3

// Arm installs the SIGUSR1 handler; profiles go to dir (created 0700).
func Arm(dir string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	var busy atomic.Bool
	go func() {
		for range ch {
			if busy.Swap(true) {
				continue
			}
			go func() {
				defer busy.Store(false)
				_ = Capture(dir, Window)
			}()
		}
	}()
}

// Capture writes cpu-, heap- and goroutines- files stamped with one time.
func Capture(dir string, window time.Duration) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	create := func(kind string) (*os.File, error) {
		return os.OpenFile(filepath.Join(dir, kind+"-"+stamp+".pprof"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	}
	cpu, err := create("cpu")
	if err != nil {
		return err
	}
	if err := pprof.StartCPUProfile(cpu); err != nil {
		cpu.Close()
		return err
	}
	time.Sleep(window)
	pprof.StopCPUProfile()
	cpu.Close()
	for _, kind := range []string{"heap", "goroutine"} {
		f, err := create(kind)
		if err != nil {
			return err
		}
		debug := 0
		if kind == "goroutine" {
			debug = 1
		}
		_ = pprof.Lookup(kind).WriteTo(f, debug)
		f.Close()
	}
	prune(dir)
	return nil
}

// prune keeps the newest keep stamps of each kind.
func prune(dir string) {
	for _, kind := range []string{"cpu", "heap", "goroutine"} {
		files, _ := filepath.Glob(filepath.Join(dir, kind+"-*.pprof"))
		sort.Strings(files)
		for len(files) > keep {
			_ = os.Remove(files[0])
			files = files[1:]
		}
	}
}
