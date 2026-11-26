package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const ManagedLaunchProtocol = "pipe-permit-v1"

// The kernel kills a pre-permit gate when its spawning thread dies. Only a
// committed owner proof permits clearing that signal and executing the target.
const permitGate = `import ctypes,os,sys
if os.read(3,1)!=b'1': sys.exit(125)
os.close(3)
try: os.set_inheritable(4,True)
except OSError: pass
if ctypes.CDLL(None).prctl(1,0,0,0,0)!=0: sys.exit(126)
os.execvpe(sys.argv[1],sys.argv[1:],os.environ)
`

func permitCommand(target *exec.Cmd) (*exec.Cmd, *os.File, *os.File, error) {
	if target.Err != nil {
		return nil, nil, nil, target.Err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	args := append([]string{"-I", "-S", "-c", permitGate, target.Path}, target.Args[1:]...)
	cmd := exec.Command("/usr/bin/python3", args...)
	cmd.ExtraFiles = []*os.File{reader}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	return cmd, reader, writer, nil
}

type OwnerIdentity struct {
	PID        int    `json:"pid"`
	BootID     string `json:"boot_id"`
	StartTicks uint64 `json:"start_ticks"`
}

func CurrentOwnerIdentity() (OwnerIdentity, error) {
	pid := os.Getpid()
	boot, ticks, err := processIdentity(pid)
	return OwnerIdentity{PID: pid, BootID: boot, StartTicks: ticks}, err
}

func (owner OwnerIdentity) Gone() (bool, error) {
	if owner.PID <= 0 || owner.BootID == "" || owner.StartTicks == 0 {
		return false, errors.New("unbound supervisor identity")
	}
	boot, err := bootIdentity()
	if err != nil {
		return false, err
	}
	if boot != owner.BootID {
		return true, nil
	}
	_, ticks, err := processIdentity(owner.PID)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return ticks != owner.StartTicks, nil
}

func launchKey(id string) string { return "job_launches/v1/" + id }

func (m *Manager) persistLaunch(s Snapshot) error {
	if m.database == nil {
		return errors.New("managed launch requires SQLite")
	}
	if s.External == nil && (s.PID <= 0 || s.BootID == "" || s.StartTicks == 0) {
		return errors.New("managed launch owner unbound")
	}
	if s.External != nil && (!validExternal(s.External) || s.PID != 0 || s.BootID != "" || s.StartTicks != 0) {
		return errors.New("invalid external launch owner binding")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	created, err := m.database.PutIfAbsent(launchKey(s.ID), raw)
	if err == nil && !created {
		err = errors.New("managed launch identity already used")
	}
	return err
}

// A missing mutable receipt never loses the write-ahead owner proof. No proof
// is removed by job reaping. This callback grants compensation, not success.
func (m *Manager) WithMissingReceipt(root, id string, supervisor OwnerIdentity, aborted bool, action func(*Snapshot) error) error {
	return m.withMissingReceipt(root, id, supervisor.Gone, aborted, action)
}

func (m *Manager) withMissingReceipt(root, id string, supervisorGone func() (bool, error), aborted bool, action func(*Snapshot) error) error {
	return m.WithIdleProject(root, func() error {
		if m.database == nil || !validReservedID(id) {
			return errors.New("managed launch authority unavailable")
		}
		raw, err := m.database.Get(launchKey(id))
		if os.IsNotExist(err) {
			gone, goneErr := supervisorGone()
			if goneErr != nil {
				return goneErr
			}
			if !gone && !aborted {
				return errors.New("launch supervisor still alive")
			}
			// A supervisor may have committed proof after the first read and
			// before dying. Only this read, AFTER the death fence, is final.
			raw, err = m.database.Get(launchKey(id))
			if !os.IsNotExist(err) {
				if err != nil {
					return err
				}
			} else {
				// Aborted means Start returned after killing/waiting the gate.
				// Otherwise kernel PDEATHSIG proves the pre-permit gate is dead.
				return action(nil)
			}
		}
		if err != nil {
			return err
		}
		var s Snapshot
		if err = json.Unmarshal(raw, &s); err != nil {
			return err
		}
		if s.ID != id || s.Workdir != root {
			return errors.New("launch owner identity mismatch")
		}
		if s.External != nil {
			if !validExternal(s.External) || s.PID != 0 || s.BootID != "" || s.StartTicks != 0 {
				return errors.New("invalid external launch proof: HOLD")
			}
			j := &Job{ID: s.ID, Command: s.Command, CommandClass: s.CommandClass, Args: append([]string(nil), s.Args...), Workdir: s.Workdir, State: Unknown, StartedAt: s.StartedAt, External: cloneExternal(s.External), OutputLimitBytes: s.OutputLimitBytes, TimeoutSeconds: s.TimeoutSeconds, MemoryLimitBytes: s.MemoryLimitBytes, CPUTimeLimitSeconds: s.CPUTimeLimitSeconds, ProcessLimit: s.ProcessLimit, CPUQuotaPercent: s.CPUQuotaPercent, stdoutPath: filepath.Join(m.root, s.ID, "stdout.log"), stderrPath: filepath.Join(m.root, s.ID, "stderr.log")}
			if j.OutputLimitBytes <= 0 || j.OutputLimitBytes > MaxJobOutputLimit {
				return errors.New("invalid external proof output bound: HOLD")
			}
			if err = m.persist(snapshot(j)); err != nil {
				return err
			}
			m.jobs[id] = j
			for _, path := range []string{j.stdoutPath, j.stderrPath} {
				f, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
				if openErr != nil {
					return openErr
				}
				if closeErr := f.Close(); closeErr != nil {
					return closeErr
				}
			}
			observed, err := m.observeExternal(j, false)
			if err != nil {
				return err
			}
			if !isTerminal(observed.State) || !observed.ResourcesReleased || observed.ReconciledAt.IsZero() || time.Since(observed.ReconciledAt) > time.Minute {
				return errors.New("external launch resources not terminal and fenced: HOLD")
			}
			return action(&observed)
		}
		j := &Job{ID: s.ID, PID: s.PID, BootID: s.BootID, StartTicks: s.StartTicks, ScopeUnit: s.ScopeUnit}
		gone, err := unknownGroupGone(j)
		if err != nil {
			return err
		}
		if !gone {
			return errors.New("launch owner resources remain")
		}
		return action(&s)
	})
}
