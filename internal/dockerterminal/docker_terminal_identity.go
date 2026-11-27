package dockerterminal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"mcpshka/internal/containerterminal"
)

const supervisorCaps = uint64(1<<unix.CAP_KILL | 1<<unix.CAP_SETUID | 1<<unix.CAP_SETGID)

func parseSupervisorStatus(raw string) error {
	uidOK, capsOK, tracerOK := false, false, false
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "Uid:":
			if len(f) != 5 {
				return errors.New("invalid supervisor UID record")
			}
			uidOK = true
			for _, s := range f[1:] {
				uid, e := strconv.Atoi(s)
				if e != nil || uid != containerterminal.SupervisorUID {
					uidOK = false
				}
			}
		case "CapEff:":
			caps, e := strconv.ParseUint(f[1], 16, 64)
			capsOK = e == nil && caps == supervisorCaps
		case "TracerPid:":
			tracerOK = f[1] == "0"
		}
	}
	if !uidOK || !capsOK || !tracerOK {
		return errors.New("supervisor credentials or tracer unverified: HOLD")
	}
	return nil
}

func kernelBirth(pid int) (uint64, error) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return 0, e
	}
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return 0, errors.New("invalid supervisor birth")
	}
	f := strings.Fields(string(b[i+2:]))
	if len(f) < 20 {
		return 0, errors.New("short supervisor birth")
	}
	return strconv.ParseUint(f[19], 10, 64)
}

func (m *terminalManager) verifySupervisor(ctx context.Context, r *terminalReceipt) error {
	var x struct {
		ID, ContainerID string
		Running         bool
		Pid             int
	}
	if e := m.engine.json(ctx, "GET", "/exec/"+r.Status.ExecID+"/json", nil, &x); e != nil {
		return e
	}
	if !x.Running || x.Pid <= 0 || x.ID != r.Status.ExecID || x.ContainerID != r.ContainerID {
		return errors.New("supervisor launch not running: HOLD")
	}
	var container struct {
		ID    string `json:"Id"`
		State struct {
			Running bool
			Pid     int
		}
	}
	if e := m.engine.json(ctx, "GET", "/containers/"+r.ContainerID+"/json", nil, &container); e != nil {
		return e
	}
	if container.ID != r.ContainerID || !container.State.Running || container.State.Pid <= 0 {
		return errors.New("container namespace identity unavailable: HOLD")
	}
	guardNS, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", x.Pid))
	if e != nil {
		return e
	}
	containerNS, e := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", container.State.Pid))
	if e != nil {
		return e
	}
	if guardNS != containerNS {
		return errors.New("supervisor outside pinned container namespace: HOLD")
	}
	fd, e := unix.PidfdOpen(x.Pid, 0)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	birth, e := kernelBirth(x.Pid)
	if e != nil {
		return e
	}
	f, e := os.Open(fmt.Sprintf("/proc/%d/exe", x.Pid))
	if e != nil {
		return fmt.Errorf("executed helper inspection unavailable: %w", e)
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, 8<<20+1))
	f.Close()
	if e != nil {
		return e
	}
	if n > 8<<20 || hex.EncodeToString(h.Sum(nil)) != r.HelperSHA {
		return errors.New("executed helper hash mismatch: HOLD")
	}
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", x.Pid))
	if e != nil {
		return e
	}
	if e = parseSupervisorStatus(string(b)); e != nil {
		return e
	}
	var again struct {
		ID, ContainerID string
		Running         bool
		Pid             int
	}
	if e = m.engine.json(ctx, "GET", "/exec/"+r.Status.ExecID+"/json", nil, &again); e != nil {
		return e
	}
	second, e := kernelBirth(x.Pid)
	if e != nil {
		return e
	}
	if !again.Running || again.Pid != x.Pid || again.ID != x.ID || again.ContainerID != x.ContainerID || second != birth {
		return errors.New("supervisor identity changed: HOLD")
	}
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	ready, e := unix.Poll(poll, 0)
	if e != nil {
		return e
	}
	if ready != 0 {
		return errors.New("supervisor exited during verification: HOLD")
	}
	r.KernelPID = x.Pid
	r.StartTicks = birth
	r.ExecutedVerified = true
	return nil
}
