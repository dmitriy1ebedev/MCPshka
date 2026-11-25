package dockerterminal

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/brokerdocker"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mcpshka/internal/containerterminal"
	"mcpshka/internal/protectedcore"
)

const terminalHelper = "/usr/local/libexec/mcpshka-container-terminal"

// RootOwned checks that the helper is root-owned and not writable; the broker
// wires its own check, unwired it refuses.
var RootOwned = func(string) error { return errors.New("docker terminal: helper check not wired") }

type TerminalInput struct {
	Action    string `json:"action"`
	Owner     string `json:"owner"`
	Container string `json:"container,omitempty"`
	ID        string `json:"id,omitempty"`
	Key       string `json:"idempotency_key,omitempty"`
	Data      string `json:"data_base64,omitempty"`
	Cursor    int64  `json:"cursor,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	Columns   int    `json:"columns,omitempty"`
}

type TerminalStatus struct {
	ContainerID string `json:"container_id,omitempty"`
	Trust       string `json:"trust,omitempty"`
	ID          string `json:"id,omitempty"`
	Container   string `json:"container,omitempty"`
	ExecID      string `json:"exec_id,omitempty"`
	State       string `json:"state"`
	ShellUID    int    `json:"shell_uid"`
	Filesystem  string `json:"filesystem"`
	Fenced      bool   `json:"fenced"`
	ExitCode    int    `json:"exit_code,omitempty"`
	Cursor      int64  `json:"cursor,omitempty"`
	NextCursor  int64  `json:"next_cursor,omitempty"`
	Data        string `json:"data_base64,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	NextAction  string `json:"next_safe_action,omitempty"`
}

type terminalReceipt struct {
	KernelPID         int               `json:"kernel_pid,omitempty"`
	StartTicks        uint64            `json:"start_ticks,omitempty"`
	ExecutedVerified  bool              `json:"executed_verified"`
	Owner             string            `json:"owner"`
	ContainerID       string            `json:"container_id"`
	HelperSHA         string            `json:"helper_sha256"`
	Status            TerminalStatus    `json:"status"`
	StartAcknowledged bool              `json:"start_acknowledged"`
	Operations        map[string]string `json:"operations"`
}

type liveTerminal struct {
	conn   net.Conn
	mu     sync.Mutex
	output []byte
	base   int64
	ended  bool
}
type terminalManager struct {
	mu     sync.Mutex
	root   string
	engine *dockerEngine
	live   map[string]*liveTerminal
	uid    uint32
}

var dockerTerminals = &terminalManager{root: "/var/lib/mcpshka-executor/docker-terminals", engine: newDockerEngine(), live: map[string]*liveTerminal{}}

func terminalHex(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(s) == n && len(b)*2 == n && strings.ToLower(s) == s
}

func (m *terminalManager) privateDir() error {
	parent, err := os.Lstat(filepath.Dir(m.root))
	if err != nil {
		return err
	}
	p, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || p.Uid != m.uid || parent.Mode().Perm()&0077 != 0 {
		return errors.New("terminal parent unverified: HOLD")
	}
	if err = os.Mkdir(m.root, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(m.root)
	if err != nil {
		return err
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || s.Uid != m.uid || st.Mode().Perm() != 0700 {
		return errors.New("terminal state directory unverified: HOLD")
	}
	return nil
}

func (m *terminalManager) load(id string) (terminalReceipt, error) {
	var r terminalReceipt
	if !terminalHex(id, 32) {
		return r, errors.New("invalid terminal ID")
	}
	p := filepath.Join(m.root, id+".json")
	st, e := os.Lstat(p)
	if e != nil {
		return r, e
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || s.Uid != m.uid || s.Nlink != 1 || st.Mode().Perm() != 0600 || st.Size() > 65536 {
		return r, errors.New("terminal receipt unverified: HOLD")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.Status.ID != id || r.Owner == "" || !brokerdocker.ValidContainer(r.Status.Container) || r.Operations == nil {
		return r, errors.New("terminal receipt identity unverified: HOLD")
	}
	return r, nil
}

func (m *terminalManager) save(r terminalReceipt) error {
	b, e := json.Marshal(r)
	if e != nil || len(b) > 65536 {
		return errors.New("terminal receipt budget exceeded")
	}
	f, e := os.CreateTemp(m.root, ".receipt-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(name, filepath.Join(m.root, r.Status.ID+".json")); e != nil {
		return e
	}
	d, e := os.Open(m.root)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

func (m *terminalManager) reconcile(ctx context.Context, r *terminalReceipt) error {
	if r.Status.ExecID == "" {
		return nil
	}
	var x struct {
		ID, ContainerID string
		Running         bool
		ExitCode, Pid   int
	}
	if e := m.engine.json(ctx, "GET", "/exec/"+r.Status.ExecID+"/json", nil, &x); e != nil {
		return e
	}
	if x.ID != r.Status.ExecID || x.ContainerID != r.ContainerID {
		return errors.New("Docker exec identity changed: HOLD")
	}
	if !x.Running && r.StartAcknowledged && r.ExecutedVerified && x.Pid == r.KernelPID && x.Pid > 0 {
		r.Status.ExitCode = x.ExitCode
		if x.ExitCode == 0 {
			r.Status.State = "completed"
			r.Status.Fenced = true
			r.Status.NextAction = "none"
		} else {
			r.Status.State = "hold"
			r.Status.NextAction = "independently verify descendants; do not release writer scope"
		}
		return m.save(*r)
	}
	if x.Running {
		r.Status.State = "running"
		if m.live[r.Status.ID] == nil {
			r.Status.State = "unknown"
			r.Status.NextAction = "wait for supervisor expiry; reconcile exec; never start a replacement"
		}
	}
	return nil
}

func (m *terminalManager) readOutput(s *TerminalStatus, in TerminalInput) {
	l := m.live[s.ID]
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	c := in.Cursor
	if c < l.base {
		c = l.base
		s.Truncated = true
	}
	if c > l.base+int64(len(l.output)) {
		c = l.base + int64(len(l.output))
	}
	s.Cursor = c
	s.NextCursor = l.base + int64(len(l.output))
	s.Data = base64.StdEncoding.EncodeToString(l.output[c-l.base:])
}

func (m *terminalManager) scope(ctx context.Context, owner, container string) (TerminalStatus, error) {
	var current struct {
		ID string `json:"Id"`
	}
	if e := m.engine.json(ctx, "GET", "/containers/"+container+"/json", nil, &current); e != nil {
		return TerminalStatus{}, e
	}
	if !terminalHex(current.ID, 64) {
		return TerminalStatus{}, errors.New("container identity unverified")
	}
	entries, e := os.ReadDir(m.root)
	if e != nil {
		return TerminalStatus{}, e
	}
	if len(entries) > 128 {
		return TerminalStatus{}, errors.New("terminal record budget: HOLD")
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			return TerminalStatus{}, errors.New("unknown terminal artifact: HOLD")
		}
		r, e := m.load(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return TerminalStatus{}, e
		}
		if r.ContainerID != current.ID || r.Status.Fenced {
			continue
		}
		if e = m.reconcile(ctx, &r); e != nil {
			return TerminalStatus{}, e
		}
		if r.Status.Fenced {
			continue
		}
		if r.Owner != owner {
			return TerminalStatus{Container: container, State: "hold", NextAction: "container has another writer"}, nil
		}
		r.Status.ContainerID = r.ContainerID
		return r.Status, nil
	}
	return TerminalStatus{Container: container, ContainerID: current.ID, State: "free", Filesystem: "read_only"}, nil
}

func (m *terminalManager) shellSlot() (int, error) {
	entries, e := os.ReadDir(m.root)
	if e != nil {
		return 0, e
	}
	if len(entries) >= 128 {
		return 0, errors.New("terminal receipt budget exhausted: HOLD")
	}
	used := map[int]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			return 0, errors.New("unknown terminal artifact: HOLD")
		}
		r, e := m.load(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return 0, e
		}
		if !r.Status.Fenced {
			used[r.Status.ShellUID] = true
		}
	}
	processes, e := os.ReadDir("/proc")
	if e != nil {
		return 0, e
	}
	for _, entry := range processes {
		if _, e := strconv.Atoi(entry.Name()); e != nil {
			continue
		}
		b, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		// A process exiting between ReadDir and ReadFile answers ESRCH, not
		// ENOENT (seen on a busy GitVerse runner, BUG-20261023-63).
		if os.IsNotExist(e) || errors.Is(e, syscall.ESRCH) {
			continue
		}
		if e != nil {
			return 0, errors.New("kernel UID reservation unverified: HOLD")
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(line, "Uid:") {
				continue
			}
			f := strings.Fields(line)
			if len(f) != 5 {
				return 0, errors.New("kernel UID identity malformed: HOLD")
			}
			for _, s := range f[1:] {
				uid, e := strconv.Atoi(s)
				if e != nil {
					return 0, e
				}
				if uid >= containerterminal.FirstShellUID && uid < containerterminal.FirstShellUID+containerterminal.ShellSlots {
					used[uid] = true
				}
			}
		}
	}
	for uid := containerterminal.FirstShellUID; uid < containerterminal.FirstShellUID+containerterminal.ShellSlots; uid++ {
		if !used[uid] {
			return uid, nil
		}
	}
	return 0, errors.New("terminal resource slots occupied: HOLD")
}

func (m *terminalManager) helper(ctx context.Context, name string) (string, string, error) {
	dumpable, e := os.ReadFile("/proc/sys/fs/suid_dumpable")
	if e != nil || strings.TrimSpace(string(dumpable)) != "0" {
		return "", "", errors.New("privileged executable dump protection unverified: HOLD")
	}
	if e := RootOwned(terminalHelper); e != nil {
		return "", "", e
	}
	f, e := os.Open(terminalHelper)
	if e != nil {
		return "", "", e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return "", "", e
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Nlink != 1 || st.Size() > 8<<20 {
		return "", "", errors.New("unsafe terminal helper: HOLD")
	}
	b, e := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if e != nil || len(b) > 8<<20 {
		return "", "", errors.New("terminal helper budget")
	}
	sha := sha256.Sum256(b)
	hash := hex.EncodeToString(sha[:])
	path := "/.mcpshka-terminal-" + hash
	var inspect struct {
		ID         string `json:"Id"`
		State      struct{ Running bool }
		HostConfig struct {
			Privileged  bool
			PidMode     string
			CapDrop     []string
			CapAdd      []string
			SecurityOpt []string
		}
		Mounts []struct{ Destination, Source string }
	}
	if e = m.engine.json(ctx, "GET", "/containers/"+name+"/json", nil, &inspect); e != nil {
		return "", "", e
	}
	if !terminalHex(inspect.ID, 64) || !inspect.State.Running || inspect.HostConfig.Privileged || inspect.HostConfig.PidMode != "" {
		return "", "", errors.New("container terminal launch isolation denied")
	}
	for _, cap := range inspect.HostConfig.CapAdd {
		c := strings.TrimPrefix(strings.ToUpper(cap), "CAP_")
		if c == "ALL" || c == "SYS_ADMIN" || c == "SYS_PTRACE" || c == "SYS_RAWIO" || c == "SYS_MODULE" || c == "SYS_BOOT" {
			return "", "", errors.New("unsafe added container capability")
		}
	}
	for _, option := range inspect.HostConfig.SecurityOpt {
		if strings.Contains(option, "unconfined") || strings.HasPrefix(option, "no-new-privileges") {
			return "", "", errors.New("container security profile incompatible with protected supervisor")
		}
	}
	for _, cap := range inspect.HostConfig.CapDrop {
		if cap == "ALL" || cap == "KILL" || cap == "SETUID" || cap == "SETGID" {
			return "", "", errors.New("terminal supervisor capabilities unavailable")
		}
	}
	for _, mount := range inspect.Mounts {
		d := filepath.Clean(mount.Destination)
		s := filepath.Clean(mount.Source)
		if s == "/" || s == "/proc" || strings.HasPrefix(s, "/proc/") || s == "/sys" || strings.HasPrefix(s, "/sys/") || s == "/root" || strings.HasPrefix(s, "/root/") || s == "/dev/mem" || s == "/dev/kmsg" || s == "/run/docker.sock" || s == "/var/run/docker.sock" || protectedcore.RuntimePathProtected(s) {
			return "", "", errors.New("sensitive host mount source: terminal denied")
		}
		for _, component := range protectedcore.Components() {
			for _, protected := range component.RuntimePaths {
				if strings.HasPrefix(protected, s+"/") {
					return "", "", errors.New("host mount contains protected core: terminal denied")
				}
			}
		}
		if d == "/" || d == "/proc" || d == "/dev" || d == "/run/docker.sock" || d == "/var/run/docker.sock" || strings.HasPrefix(path, d+"/") {
			return "", "", errors.New("sensitive container mount: terminal denied")
		}
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	capability := make([]byte, 20)
	binary.LittleEndian.PutUint32(capability, 0x02000001)
	binary.LittleEndian.PutUint32(capability[4:], uint32(supervisorCaps))
	if e = tw.WriteHeader(&tar.Header{Name: strings.TrimPrefix(path, "/"), Mode: 0555, Uid: 0, Gid: 0, Size: int64(len(b)), Typeflag: tar.TypeReg, Xattrs: map[string]string{"security.capability": string(capability)}}); e != nil {
		return "", "", e
	}
	if _, e = tw.Write(b); e != nil {
		return "", "", e
	}
	if e = tw.Close(); e != nil {
		return "", "", e
	}
	if e = m.engine.request(ctx, "PUT", "/containers/"+inspect.ID+"/archive?path=/&noOverwriteDirNonDir=true", "application/x-tar", &archive, nil); e != nil {
		return "", "", e
	}
	return inspect.ID, path, nil
}

func (m *terminalManager) run(ctx context.Context, allowed []string, in TerminalInput) (TerminalStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var zero TerminalStatus
	if in.Owner == "" || len(in.Owner) > 256 {
		return zero, errors.New("terminal owner required")
	}
	if e := m.privateDir(); e != nil {
		return zero, e
	}
	if in.Action == "status" && in.ID == "" {
		if !brokerdocker.ValidContainer(in.Container) || !slices.Contains(allowed, in.Container) {
			return zero, errors.New("terminal container not allowlisted")
		}
		return m.scope(ctx, in.Owner, in.Container)
	}
	if in.Action == "open" {
		if !brokerdocker.ValidContainer(in.Container) || !slices.Contains(allowed, in.Container) || in.Key == "" || len(in.Key) > 128 {
			return zero, errors.New("terminal open denied")
		}
		sum := sha256.Sum256([]byte(in.Owner + "\x00" + in.Key))
		id := hex.EncodeToString(sum[:16])
		if r, e := m.load(id); e == nil {
			if r.Owner != in.Owner || r.Status.Container != in.Container {
				return zero, errors.New("foreign terminal denied")
			}
			if e = m.reconcile(ctx, &r); e != nil {
				return zero, e
			}
			return r.Status, nil
		} else if !os.IsNotExist(e) {
			return zero, e
		}
		scope, e := m.scope(ctx, in.Owner, in.Container)
		if e != nil {
			return zero, e
		}
		if scope.State != "free" {
			return scope, errors.New("container writer occupied: HOLD")
		}
		uid, e := m.shellSlot()
		if e != nil {
			return zero, e
		}
		containerID, helper, e := m.helper(ctx, in.Container)
		if e != nil {
			return zero, e
		}
		if containerID != scope.ContainerID {
			return zero, errors.New("container replaced during admission: HOLD")
		}
		r := terminalReceipt{Owner: in.Owner, ContainerID: containerID, HelperSHA: strings.TrimPrefix(helper, "/.mcpshka-terminal-"), Status: TerminalStatus{ID: id, Container: in.Container, ContainerID: containerID, Trust: "UNTRUSTED", State: "unknown", ShellUID: uid, Filesystem: "read_only", NextAction: "reconcile dispatch; never repeat unknown creation"}, Operations: map[string]string{}}
		if e = m.save(r); e != nil {
			return zero, e
		}
		var created struct {
			ID string `json:"Id"`
		}
		input := map[string]any{"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true, "Privileged": false, "User": "65533:65533", "Cmd": []string{helper, id, strconv.Itoa(uid)}, "Env": []string{"GOMAXPROCS=2", "GOMEMLIMIT=32MiB"}}
		if e = m.engine.json(ctx, "POST", "/containers/"+containerID+"/exec", input, &created); e != nil {
			return r.Status, e
		}
		if !terminalHex(created.ID, 64) {
			return r.Status, errors.New("exec identity unverified: HOLD")
		}
		r.Status.ExecID = created.ID
		if e = m.save(r); e != nil {
			return r.Status, e
		}
		conn, reader, e := m.engine.attach(ctx, created.ID)
		if e != nil {
			return r.Status, e
		}
		if e = m.verifySupervisor(ctx, &r); e != nil {
			conn.Close()
			return r.Status, e
		}
		l := &liveTerminal{conn: conn}
		m.live[id] = l
		r.StartAcknowledged = true
		r.Status.State = "running"
		r.Status.NextAction = "read output or close session"
		if e = m.save(r); e != nil {
			conn.Close()
			return r.Status, e
		}
		go func() {
			defer conn.Close()
			b := make([]byte, 4096)
			for {
				n, e := reader.Read(b)
				if n > 0 {
					l.mu.Lock()
					l.output = append(l.output, b[:n]...)
					if len(l.output) > 32768 {
						drop := len(l.output) - 32768
						l.output = l.output[drop:]
						l.base += int64(drop)
					}
					l.mu.Unlock()
				}
				if e != nil {
					l.mu.Lock()
					l.ended = true
					l.mu.Unlock()
					return
				}
			}
		}()
		return r.Status, nil
	}
	r, e := m.load(in.ID)
	if e != nil {
		return zero, e
	}
	if r.Owner != in.Owner {
		return zero, errors.New("foreign terminal denied")
	}
	if !slices.Contains(allowed, r.Status.Container) {
		return zero, errors.New("terminal capability revoked: HOLD")
	}
	if e = m.reconcile(ctx, &r); e != nil {
		return zero, e
	}
	if in.Action == "status" {
		m.readOutput(&r.Status, in)
		return r.Status, nil
	}
	if in.Key == "" || len(in.Key) > 128 {
		return zero, errors.New("terminal idempotency key required")
	}
	b, e := json.Marshal(in)
	if e != nil {
		return zero, e
	}
	fp := sha256.Sum256(b)
	fingerprint := hex.EncodeToString(fp[:])
	if previous, ok := r.Operations[in.Key]; ok {
		if previous != "done:"+fingerprint {
			return r.Status, errors.New("terminal operation UNKNOWN or key conflict; do not resend")
		}
		m.readOutput(&r.Status, in)
		return r.Status, nil
	}
	if r.Status.Fenced {
		if in.Action == "close" {
			return r.Status, nil
		}
		return r.Status, errors.New("terminal completed")
	}
	l := m.live[in.ID]
	if l == nil {
		return r.Status, errors.New("terminal transport lost: UNKNOWN")
	}
	var data []byte
	switch in.Action {
	case "write":
		data, e = base64.StdEncoding.DecodeString(in.Data)
		if e != nil || len(data) > 2048 {
			return zero, errors.New("terminal input budget")
		}
	case "close":
		data = []byte{29}
	case "resize":
		if in.Rows < 1 || in.Rows > 256 || in.Columns < 1 || in.Columns > 256 {
			return zero, errors.New("invalid terminal dimensions")
		}
	default:
		return zero, errors.New("unknown terminal action")
	}
	if len(r.Operations) >= 128 && in.Action != "close" {
		return r.Status, errors.New("terminal operation budget: HOLD")
	}
	r.Operations[in.Key] = "intent:" + fingerprint
	if e = m.save(r); e != nil {
		return r.Status, e
	}
	if in.Action == "resize" {
		e = m.engine.json(ctx, "POST", fmt.Sprintf("/exec/%s/resize?h=%d&w=%d", r.Status.ExecID, in.Rows, in.Columns), nil, nil)
	} else {
		_ = l.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		var n int
		n, e = l.conn.Write(data)
		if e == nil && n != len(data) {
			e = io.ErrShortWrite
		}
		_ = l.conn.SetWriteDeadline(time.Time{})
	}
	if e != nil {
		r.Status.State = "unknown"
		return r.Status, e
	}
	r.Operations[in.Key] = "done:" + fingerprint
	if in.Action == "close" {
		r.Status.State = "closing"
		r.Status.NextAction = "poll status until supervisor exits successfully; closing is not fencing"
	}
	if e = m.save(r); e != nil {
		return r.Status, e
	}
	m.readOutput(&r.Status, in)
	return r.Status, nil
}

func Run(ctx context.Context, allowed []string, args []string) ([]byte, error) {
	if len(args) != 1 || len(args[0]) > 4096 {
		return nil, errors.New("invalid terminal request")
	}
	var in TerminalInput
	d := json.NewDecoder(strings.NewReader(args[0]))
	d.DisallowUnknownFields()
	if e := d.Decode(&in); e != nil {
		return nil, e
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("one terminal request required")
	}
	out, e := dockerTerminals.run(ctx, allowed, in)
	b, marshal := json.Marshal(out)
	if e != nil {
		return b, e
	}
	return b, marshal
}
