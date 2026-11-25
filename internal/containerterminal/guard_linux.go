// Package containerterminal supervises an unprivileged shell inside a container.
package containerterminal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const SupervisorUID = 65533
const FirstShellUID = 60000
const ShellSlots = 8

// Shell performs the final unprivileged exec after setting hard inherited limits.
func Shell() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Getuid() < FirstShellUID || os.Getuid() >= FirstShellUID+ShellSlots || os.Geteuid() != os.Getuid() {
		return errors.New("invalid shell identity")
	}
	var caps [2]unix.CapUserData
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	if e := unix.Capset(&header, &caps[0]); e != nil {
		return e
	}
	if e := unix.Capget(&header, &caps[0]); e != nil {
		return e
	}
	if caps[0].Effective != 0 || caps[1].Effective != 0 {
		return errors.New("shell capabilities not empty")
	}
	if value, _, errno := unix.RawSyscall6(unix.SYS_PRCTL, unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0, 0); errno != 0 || value != 1 {
		if errno == 0 {
			return errors.New("shell requires no-new-privileges")
		}
		return errno
	}
	path, e := unix.BytePtrFromString("/bin/sh")
	if e != nil {
		return e
	}
	arg0, _ := unix.BytePtrFromString("/bin/sh")
	arg1, _ := unix.BytePtrFromString("-i")
	argv := []*byte{arg0, arg1, nil}
	var env []*byte
	for _, s := range []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "TERM=xterm-256color"} {
		p, e := unix.BytePtrFromString(s)
		if e != nil {
			return e
		}
		env = append(env, p)
	}
	env = append(env, nil)
	for resource, limit := range map[int]uint64{unix.RLIMIT_CPU: 20, unix.RLIMIT_NPROC: 8, unix.RLIMIT_NOFILE: 64, unix.RLIMIT_CORE: 0, unix.RLIMIT_FSIZE: 0} {
		if e := unix.Setrlimit(resource, &unix.Rlimit{Cur: limit, Max: limit}); e != nil {
			return e
		}
	}
	if e := unix.Setrlimit(unix.RLIMIT_AS, &unix.Rlimit{Cur: 64 << 20, Max: 64 << 20}); e != nil {
		return e
	}
	_, _, errno := unix.RawSyscall(unix.SYS_EXECVE, uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&argv[0])), uintptr(unsafe.Pointer(&env[0])))
	runtime.KeepAlive(argv)
	runtime.KeepAlive(env)
	if errno != 0 {
		return errno
	}
	return errors.New("exec unexpectedly returned")
}

type process struct {
	pid, parent int
	start       uint64
	cpu         uint64
	rss         int64
}

func parseStat(pid int, raw string) (process, error) {
	i := strings.LastIndex(raw, ") ")
	if i < 0 {
		return process{}, errors.New("invalid process identity")
	}
	f := strings.Fields(raw[i+2:])
	if len(f) < 22 {
		return process{}, errors.New("short process identity")
	}
	parent, e1 := strconv.Atoi(f[1])
	user, e2 := strconv.ParseUint(f[11], 10, 64)
	system, e3 := strconv.ParseUint(f[12], 10, 64)
	childUser, e6 := strconv.ParseUint(f[13], 10, 64)
	childSystem, e7 := strconv.ParseUint(f[14], 10, 64)
	start, e4 := strconv.ParseUint(f[19], 10, 64)
	rss, e5 := strconv.ParseInt(f[21], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || e7 != nil || rss < 0 {
		return process{}, errors.New("invalid process fields")
	}
	return process{pid: pid, parent: parent, start: start, cpu: user + system + childUser + childSystem, rss: rss * int64(os.Getpagesize())}, nil
}

func descendants(root int) ([]process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	all := map[int]process{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		p, err := parseStat(pid, string(b))
		if err != nil {
			return nil, err
		}
		all[pid] = p
	}
	owned := map[int]bool{root: true}
	var out []process
	for changed := true; changed; {
		changed = false
		for pid, p := range all {
			if !owned[pid] && owned[p.parent] {
				owned[pid] = true
				out = append(out, p)
				changed = true
			}
		}
	}
	return out, nil
}

func fence(ps []process) error {
	for _, p := range ps {
		fd, err := unix.PidfdOpen(p.pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return err
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.pid))
		if err == nil {
			var now process
			now, err = parseStat(p.pid, string(b))
			if err == nil && now.start != p.start {
				err = errors.New("process identity changed: HOLD")
			}
		}
		if err == nil {
			err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		}
		unix.Close(fd)
		if err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return nil
}

func openPTY() (*os.File, *os.File, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	master := os.NewFile(uintptr(fd), "pty-master")
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

type countingWriter struct {
	io.Writer
	bytes *atomic.Uint64
}

func (w countingWriter) Write(b []byte) (int, error) {
	used := w.bytes.Load()
	if used >= 1<<20 {
		return 0, errors.New("terminal output limit")
	}
	limited := false
	if uint64(len(b)) > (1<<20)-used {
		b = b[:(1<<20)-used]
		limited = true
	}
	n, e := w.Writer.Write(b)
	w.bytes.Add(uint64(n))
	if e == nil && limited {
		e = errors.New("terminal output limit")
	}
	return n, e
}

// Deny filesystem mutations while retaining interactive read/execute access.
// Existing PTY descriptors are opened before restriction and remain usable.
func readOnlyFilesystem() error {
	abi, _, errno := unix.RawSyscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return errno
	}
	if abi < 3 {
		return errors.New("Landlock ABI 3 required: HOLD")
	}
	access := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	fd, _, errno := unix.RawSyscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&access)), 8, 0)
	if errno != 0 {
		return errno
	}
	defer unix.Close(int(fd))
	entries, e := os.ReadDir("/")
	if e != nil {
		return e
	}
	var paths []string
	for _, entry := range entries {
		if entry.Name() != "dev" && entry.Type()&os.ModeSymlink == 0 {
			paths = append(paths, "/"+entry.Name())
		}
	}
	paths = append(paths, "/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty")
	for _, path := range paths {
		p, e := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		attr := unix.LandlockPathBeneathAttr{Allowed_access: unix.LANDLOCK_ACCESS_FS_READ_FILE, Parent_fd: int32(p)}
		_, _, errno = unix.RawSyscall6(unix.SYS_LANDLOCK_ADD_RULE, fd, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
		unix.Close(p)
		if errno != 0 {
			return errno
		}
	}
	_, _, errno = unix.RawSyscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// Run uses a separate capability-bearing UID; the shell cannot signal it.
func Run(shellUID int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if os.Getuid() != SupervisorUID || os.Geteuid() != SupervisorUID || shellUID < FirstShellUID || shellUID >= FirstShellUID+ShellSlots {
		return errors.New("invalid supervisor or shell identity")
	}
	var caps [2]unix.CapUserData
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	if e := unix.Capget(&header, &caps[0]); e != nil {
		return e
	}
	if caps[0].Effective != uint32(1<<unix.CAP_KILL|1<<unix.CAP_SETUID|1<<unix.CAP_SETGID) || caps[1].Effective != 0 {
		return errors.New("supervisor capabilities unverified")
	}
	if st, err := os.Lstat("/.dockerenv"); err != nil || !st.Mode().IsRegular() {
		return errors.New("container marker missing: HOLD")
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if _, err := descendants(os.Getpid()); err != nil {
		return fmt.Errorf("process observation unavailable: %w", err)
	}
	probe, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return err
	}
	unix.Close(probe)
	for resource, limit := range map[int]uint64{unix.RLIMIT_CPU: 120, unix.RLIMIT_NPROC: 64, unix.RLIMIT_NOFILE: 64, unix.RLIMIT_CORE: 0} {
		if err := unix.Setrlimit(resource, &unix.Rlimit{Cur: limit, Max: limit}); err != nil {
			return err
		}
	}
	master, slave, err := openPTY()
	if err != nil {
		return err
	}
	defer master.Close()
	outer, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		slave.Close()
		return err
	}
	raw := *outer
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err = unix.IoctlSetTermios(0, unix.TCSETS, &raw); err != nil {
		slave.Close()
		return err
	}
	defer unix.IoctlSetTermios(0, unix.TCSETS, outer)
	if err = readOnlyFilesystem(); err != nil {
		slave.Close()
		return err
	}
	resize := func() {
		if w, e := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ); e == nil {
			_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, w)
		}
	}
	resize()
	cmd := exec.Command("/proc/self/exe", "shell")
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "TERM=xterm-256color", "GOMAXPROCS=2", "GOMEMLIMIT=32MiB"}
	cmd.Dir = "/tmp"
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0, Credential: &syscall.Credential{Uid: uint32(shellUID), Gid: 65534, Groups: []uint32{}}, Pdeathsig: syscall.SIGKILL}
	if err = cmd.Start(); err != nil {
		slave.Close()
		return err
	}
	slave.Close()
	defer cmd.Process.Release()
	var output atomic.Uint64
	var requestedStop atomic.Bool
	go func() {
		b := make([]byte, 4096)
		for {
			n, e := os.Stdin.Read(b)
			if n > 0 {
				if strings.ContainsRune(string(b[:n]), 29) {
					requestedStop.Store(true)
					return
				}
				if _, e = master.Write(b[:n]); e != nil {
					requestedStop.Store(true)
					return
				}
			}
			if e != nil {
				requestedStop.Store(true)
				return
			}
		}
	}()
	go func() { _, _ = io.Copy(countingWriter{os.Stdout, &output}, master); requestedStop.Store(true) }()
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGWINCH)
	defer signal.Stop(signals)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	ending := false
	for {
		select {
		case s := <-signals:
			if s == syscall.SIGWINCH {
				resize()
			} else {
				ending = true
			}
		case <-deadline.C:
			ending = true
		case <-tick.C:
		}
		noChildren := false
		for {
			var status unix.WaitStatus
			var usage unix.Rusage
			pid, e := unix.Wait4(-1, &status, unix.WNOHANG, &usage)
			if errors.Is(e, syscall.ECHILD) {
				noChildren = true
			}
			if e != nil || pid == 0 {
				break
			}
			if pid == cmd.Process.Pid {
				ending = true
			}
		}
		ps, e := descendants(os.Getpid())
		if e != nil {
			return fmt.Errorf("process fencing unverified: %w", e)
		}
		var cpu uint64
		var rss int64
		for _, p := range ps {
			cpu += p.cpu
			rss += p.rss
		}
		// Linux USER_HZ is 100; accumulated child usage includes reaped processes.
		var total unix.Rusage
		if e = unix.Getrusage(unix.RUSAGE_CHILDREN, &total); e != nil {
			return e
		}
		spent := float64(total.Utime.Sec+total.Stime.Sec) + float64(total.Utime.Usec+total.Stime.Usec)/1e6 + float64(cpu)/100
		if spent > 20 || rss > 128<<20 || len(ps) > 8 || output.Load() > 1<<20 || requestedStop.Load() {
			ending = true
		}
		if ending {
			if e = fence(ps); e != nil {
				return fmt.Errorf("process fencing unverified: %w", e)
			}
			if noChildren && len(ps) == 0 {
				return nil
			}
		}
	}
}
