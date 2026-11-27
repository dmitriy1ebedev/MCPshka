package diagtraffic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

func (d runDir) stop(ctx context.Context, wait time.Duration) (state, error) {
	s, err := d.load()
	if err != nil || terminal(s) {
		return s, err
	}
	if err := d.writeFile(stopRequest, []byte(now()+"\n")); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		pause(ctx, 500*time.Millisecond)
		s, err = d.load()
		if err != nil || terminal(s) {
			return s, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return d.forceStop(ctx, s)
}

func pause(ctx context.Context, duration time.Duration) {
	t := time.NewTimer(duration)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

type pinnedProcess struct {
	fd   int
	name string
}

func (p pinnedProcess) alive() bool {
	ready, err := unix.Poll([]unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}, 0)
	return ready == 0 || err != nil
}

func (d runDir) forceStop(ctx context.Context, s state) (state, error) {
	procs, err := d.pinProcesses(s)
	defer func() {
		for _, p := range procs {
			unix.Close(p.fd)
		}
	}()
	if err != nil {
		return nil, err
	}
	for _, sig := range []unix.Signal{unix.SIGTERM, unix.SIGKILL} {
		wait := false
		for _, p := range procs {
			if p.alive() {
				wait = true
				if err := unix.PidfdSendSignal(p.fd, sig, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
					return nil, err
				}
			}
		}
		if wait {
			pause(ctx, 500*time.Millisecond)
		}
	}
	if latest, err := d.load(); err != nil || terminal(latest) {
		return latest, err
	}
	alive, allStopped := map[string]bool{}, true
	for _, p := range procs {
		alive[p.name] = p.alive()
		allStopped = allStopped && !alive[p.name]
	}
	s["state"], s["ended_by"], s["finished_at"] = "CANCELLED", "forced_stop", now()
	s["pcap_bytes"], s["pcap_byte_cap"] = pcapBytes(d.pcaps()), pcapCap
	s["cleanup"] = map[string]any{"checked_at": now(), "observers_alive": alive, "all_stopped": allStopped}
	return s, d.save(s)
}

var trackerName = regexp.MustCompile(`^conntrack(?:6_[0-3])?$`)

func (d runDir) pinProcesses(s state) ([]pinnedProcess, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var ids struct {
		Worker    int            `json:"pid"`
		Observers map[string]int `json:"observer_pids"`
	}
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, err
	}
	procs := []pinnedProcess{}
	for name, pid := range ids.Observers {
		if name != "tcpdump" && !trackerName.MatchString(name) {
			continue
		}
		p, err := d.pin(pid, name)
		if err != nil {
			return procs, err
		}
		if p != nil {
			procs = append(procs, *p)
		}
	}
	worker, err := d.pin(ids.Worker, "worker")
	if worker != nil {
		procs = append(procs, *worker)
	}
	return procs, err
}

func (d runDir) pin(pid int, name string) (*pinnedProcess, error) {
	if pid <= 1 {
		return nil, nil
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	matched, err := d.ownsProcess(pid, name)
	p := pinnedProcess{fd: fd, name: name}
	if err != nil || !matched || !p.alive() {
		unix.Close(fd)
		return nil, err
	}
	return &p, nil
}

func (d runDir) ownsProcess(pid int, name string) (bool, error) {
	proc := "/proc/" + strconv.Itoa(pid)
	file, err := os.Open(proc + "/cmdline")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 8192))
	if err != nil || len(raw) == 0 {
		return false, err
	}
	argv := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	procFD := func(n string) string { return proc + "/fd/" + n }
	same := func(name, fd string) bool {
		want, e1 := d.fs.Lstat(name)
		have, e2 := os.Stat(procFD(fd))
		return e1 == nil && e2 == nil && os.SameFile(want, have)
	}
	if name == "worker" {
		if len(argv) == 6 && string(argv[1]) == "diag-traffic-worker" && string(argv[2]) == d.id {
			return same(".", "3"), nil
		}
		// A Python worker may still be finishing an observation started by
		// the preceding release. Check its executable arguments and run path.
		const legacy = "/usr/local/libexec/diag-traffic-route-observe"
		return len(argv) >= 5 && string(argv[1]) == legacy && string(argv[2]) == "_worker" &&
			string(argv[3]) == "--run-dir" && string(argv[4]) == filepath.Join(RunsRoot, d.id) &&
			same("worker.log", "1"), nil
	}
	if name == "tcpdump" {
		return string(argv[0]) == "/usr/bin/tcpdump" &&
			(same("traffic.pcap", "1") || same("tcpdump.stderr.log", "2")), nil
	}
	if name == "conntrack" || trackerName.MatchString(name) {
		suffix := name[len("conntrack"):]
		return string(argv[0]) == "/usr/sbin/conntrack" && same("conntrack.events"+suffix+".log", "1"), nil
	}
	return false, fmt.Errorf("unknown DIAG process %q", name)
}
