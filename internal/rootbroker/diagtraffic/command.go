// Package diagtraffic collects bounded and passive route evidence for one
// MAC-verified private IPv4 client and its discovered IPv6 addresses.
package diagtraffic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Run processes the broker's existing diag-traffic command forms.
func Run(ctx context.Context, args []string, out io.Writer) (int, error) {
	if err := Validate(args); err != nil {
		return 2, err
	}
	if os.Geteuid() != 0 {
		return 2, errors.New("DIAG traffic broker requires root")
	}
	base, err := os.OpenFile(RunsRoot, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return 3, err
	}
	defer base.Close()
	root, err := rootFor(base, "")
	if err != nil {
		return 3, err
	}
	defer root.fs.Close()
	if args[0] == "start" {
		t, _ := parseTarget(args[2], args[4], args[6])
		return start(root.fs, t, out)
	}
	f, err := openRun(root.fs, args[2])
	if err != nil {
		return 3, err
	}
	defer f.Close()
	d, err := rootFor(f, args[2])
	if err != nil {
		return 3, err
	}
	defer d.fs.Close()
	switch args[0] {
	case "report":
		raw, err := d.readFile("report.json", stateLimit)
		if err != nil {
			return 3, err
		}
		_, err = out.Write(raw)
		return 0, err
	case "stop":
		s, err := d.stop(ctx, 20*time.Second)
		if err != nil {
			return 1, err
		}
		if err := json.NewEncoder(out).Encode(s); err != nil {
			return 1, err
		}
		if cleanup, ok := s["cleanup"].(map[string]any); ok && cleanup["all_stopped"] == false {
			return 1, nil
		}
		return 0, nil
	default:
		s, err := d.load()
		if err != nil {
			return 3, err
		}
		if err := json.NewEncoder(out).Encode(s); err != nil {
			return 1, err
		}
		switch s["state"] {
		case "SUCCESS", "RUNNING", "PENDING", "CANCELLED":
			return 0, nil
		default:
			return 1, nil
		}
	}
}

func start(root *os.Root, t target, out io.Writer) (int, error) {
	id := time.Now().UTC().Format("20060102T150405Z") + "-" + randomName()[:8]
	if err := root.Mkdir(id, 0750|os.ModeSetgid); err != nil {
		return 1, err
	}
	f, err := openRun(root, id)
	if err != nil {
		return 1, err
	}
	defer f.Close()
	if err := f.Chmod(0750 | os.ModeSetgid); err != nil {
		return 1, err
	}
	d, err := rootFor(f, id)
	if err != nil {
		return 1, err
	}
	defer d.fs.Close()
	s := state{"state": "PENDING", "created_at": now(), "client": t.client, "mac": t.mac,
		"duration_seconds": t.duration, "passive_only": true}
	if err := d.save(s); err != nil {
		return 1, err
	}
	log, err := d.create("worker.log")
	if err != nil {
		return 1, err
	}
	defer log.Close()
	cmd := exec.Command("/proc/self/exe", "diag-traffic-worker", id, t.client, t.mac, strconv.Itoa(t.duration))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{f}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		s["state"], s["finished_at"], s["errors"] = "FAILED", now(), []string{err.Error()}
		return 1, errors.Join(err, d.save(s))
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_, err = fmt.Fprintf(out, "RUN %s state=RUNNING pid=%d path=%s\n", id, pid, filepath.Join(RunsRoot, id))
	return 0, err
}

// Worker uses the run directory descriptor inherited from the broker command.
func Worker(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 || len(args) != 4 || !validDiagTrafficRunID(args[0]) {
		return errors.New("invalid DIAG traffic worker invocation")
	}
	t, err := parseTarget(args[1], args[2], args[3])
	if err != nil {
		return err
	}
	f := os.NewFile(3, "diag-traffic-run")
	if f == nil {
		return errors.New("missing DIAG traffic run descriptor")
	}
	defer f.Close()
	unix.CloseOnExec(3)
	d, err := rootFor(f, args[0])
	if err != nil {
		return err
	}
	defer d.fs.Close()
	s, err := d.load()
	if err != nil {
		return err
	}
	return d.observe(ctx, t, s)
}
