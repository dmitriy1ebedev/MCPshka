package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const sandboxGuestProbe = `
import errno, os, socket
from pathlib import Path
for kind, original in C['namespaces'].items():
 assert os.readlink('/proc/self/ns/'+kind) != original, 'namespace '+kind+' exposed'
assert int(Path('/proc/self/stat').read_text().split(' ',1)[0])==os.getpid(), 'procfs PID namespace mismatch'
assert 'MCPSHKA_TEST_HOST_SENTINEL' not in os.environ, 'host environment exposed'
status=dict(line.split(':',1) for line in Path('/proc/self/status').read_text().splitlines() if ':' in line)
assert int(status['CapEff'].strip(),16)==0, 'guest capabilities retained'
assert status['NoNewPrivs'].strip()=='1', 'no_new_privs absent'
assert os.getcwd()=='/workspace'
assert Path('/workspace/input').read_text()=='workspace input'
def denied(action,label):
 try: action()
 except OSError as e:
  assert e.errno in (errno.ENOENT,errno.EACCES,errno.EROFS,errno.EPERM,errno.ENOTDIR), (label,e)
 else: raise AssertionError(label+' allowed')
denied(lambda: Path(C['outside']).read_bytes(),'outside read')
denied(lambda: Path(C['outside']).write_text('escaped'),'outside write')
denied(lambda: Path('/proc/1/root'+C['outside']).read_bytes(),'proc root escape')
os.symlink(C['outside'],'/tmp/escape')
denied(lambda: Path('/tmp/escape').read_bytes(),'symlink read')
denied(lambda: Path('/tmp/escape').write_text('escaped'),'symlink write')
os.unlink('/tmp/escape')
for p in ('/root/repos/mcpshka','/var/lib/mcpshka-executor/state.db','/run/mcpshka-broker/broker.sock','/var/run/docker.sock'):
 denied(lambda p=p: Path(p).read_bytes(),'authority '+p)
with socket.socket(socket.AF_UNIX) as s:
 denied(lambda: s.connect(C['unix']),'host unix socket')
with socket.socket() as s:
 s.settimeout(1)
 try: s.connect(('127.0.0.1',C['port']))
 except OSError: pass
 else: raise AssertionError('host TCP listener exposed')
if C['readonly']:
 for p,limit in (('/tmp',268435456),('/run',16777216)):
  v=os.statvfs(p)
  assert v.f_blocks*v.f_frsize<=limit, 'tmpfs size exceeded '+p
 denied(lambda: Path('/workspace/changed').write_text('changed'),'snapshot write')
 denied(lambda: os.chmod('/workspace/input',0o777),'snapshot chmod')
else:
 Path('/workspace/changed').write_text('private write')
print('guest-probe-ok',flush=True)
`

// This runs real workerArgs namespaces, with synthetic host canaries only.
// The project-test case executes the unchanged Go command and nested workloads;
// root admission and systemd cgroup fencing need separate acceptance.
func TestWorkerSandboxIsolationE2E(t *testing.T) {
	enabled := os.Getenv("MCPSHKA_SANDBOX_E2E")
	if enabled == "" {
		t.Skip("explicit cloud sandbox acceptance; not a daemon-context capability probe")
	}
	if enabled != "1" || os.Geteuid() == 0 {
		t.Fatal("sandbox acceptance requires explicit opt-in and non-root runner")
	}
	bwrap := os.Getenv("MCPSHKA_SANDBOX_E2E_BWRAP")
	if bwrap == "" {
		bwrap = "/usr/bin/bwrap"
	}
	info, err := os.Stat(bwrap)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("non-setuid bubblewrap required: %v", err)
	}
	host := t.TempDir()
	outside := filepath.Join(host, "protected")
	if err := os.WriteFile(outside, []byte("host unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tcp.Close() })
	unixPath := filepath.Join(host, "broker.sock")
	uds, err := net.Listen("unix", unixPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { uds.Close() })
	for _, addr := range []struct{ network, address string }{{"tcp4", tcp.Addr().String()}, {"unix", unixPath}} {
		c, e := net.DialTimeout(addr.network, addr.address, time.Second)
		if e != nil {
			t.Fatal("host control listener unavailable", e)
		}
		c.Close()
	}
	ns := map[string]string{}
	for _, kind := range []string{"user", "mnt", "pid", "net"} {
		ns[kind], err = os.Readlink("/proc/self/ns/" + kind)
		if err != nil {
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, profile, mode, fault string) {
		t.Helper()
		work := t.TempDir()
		cfg, _ := json.Marshal(map[string]any{"outside": outside, "unix": unixPath, "port": tcp.Addr().(*net.TCPAddr).Port, "namespaces": ns, "readonly": profile == "project-test-v1"})
		probe := fmt.Sprintf("import json\nC=json.loads(%q)\n", string(cfg)) + sandboxGuestProbe
		files := map[string]string{"input": "workspace input", "probe.py": probe, "Makefile": "probe:\n\tpython3 -I /workspace/probe.py\n", "package.json": "{\"scripts\":{\"probe\":\"python3 -I /workspace/probe.py\"}}"}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		commands := map[string][]string{
			"python": {"python3", "-I", "/workspace/probe.py"},
			"bash":   {"bash", "-lc", "$(printf python3) -I /workspace/probe.py"},
			"make":   {"make", "--no-print-directory", "probe"},
			"npm":    {"npm", "--offline", "--cache", "/tmp/npm-cache", "--loglevel=error", "run", "probe"},
		}
		nested := [][]string{commands[mode]}
		if profile == "project-test-v1" {
			nested = [][]string{commands["python"], commands["bash"], commands["make"], commands["npm"]}
		}
		command, _ := json.Marshal(nested)
		program := fmt.Sprintf("import json,subprocess\nfor argv in json.loads(%q): subprocess.run(argv,check=True,timeout=10)\n", string(command))
		if profile == "project-test-v1" {
			pkg := filepath.Join(work, "internal", "probe")
			if err := os.MkdirAll(pkg, 0700); err != nil {
				t.Fatal(err)
			}
			fixture := fmt.Sprintf(`package probe
import ("bytes"; "os/exec"; "testing")
func TestNestedWorkloads(t *testing.T) {
 cmd := exec.Command("python3", "-I", "-c", %q)
 cmd.Dir = "/workspace"
 out, err := cmd.CombinedOutput()
 if err != nil { t.Fatalf("nested workloads: %%v\n%%s", err, out) }
 if bytes.Count(out, []byte("guest-probe-ok")) != 4 { t.Fatalf("missing workload probes: %%s", out) }
}
`, program)
			for path, body := range map[string]string{filepath.Join(work, "go.mod"): "module mcpshka.sandbox/fixture\n\ngo 1.25.0\n", filepath.Join(pkg, "probe_test.go"): fixture} {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		workspace, err := os.Open(work)
		if err != nil {
			t.Fatal(err)
		}
		defer workspace.Close()
		st, err := workspace.Stat()
		if err != nil {
			t.Fatal(err)
		}
		identity := st.Sys().(*syscall.Stat_t)
		spec := RunSpec{Profile: profile, WorkspaceDevice: uint64(identity.Dev), WorkspaceInode: identity.Ino, Command: []string{"python3", "-I", "-c", program}}
		if profile == "project-test-v1" {
			spec.Command = []string{"go", "test", "-p", "1", "./internal/probe"}
		}
		if err := spec.validate(); err != nil {
			t.Fatal(err)
		}
		argv := workerArgs("", spec)
		end := -1
		for i, a := range argv {
			if a == "--" {
				end = i
				break
			}
		}
		if end < 0 {
			t.Fatal("worker command delimiter missing")
		}
		var extra []string
		failure := ""
		switch fault {
		case "host_mount":
			extra = []string{"--ro-bind", host, host}
			failure = "outside read allowed"
		case "host_network":
			extra = []string{"--share-net"}
			failure = "namespace net exposed"
		case "host_proc":
			for i, a := range argv[:end] {
				if a == "--proc" {
					argv = append(append(append([]string{}, argv[:i]...), "--ro-bind", "/proc", "/proc"), argv[i+2:]...)
					end++
					break
				}
			}
			failure = "procfs PID namespace mismatch"
		}
		argv = append(append(append([]string{}, argv[:end]...), extra...), argv[end:]...)
		deadline := 20 * time.Second
		if profile == "project-test-v1" {
			// A cold Go build in the sandbox takes ~1 min on a cloud runner
			// and over 3 min on a loaded ARM box (BUG-20261026-119).
			deadline = 180 * time.Second
			if d, err := time.ParseDuration(os.Getenv("MCPSHKA_SANDBOX_E2E_GO_TIMEOUT")); err == nil && d > 0 {
				deadline = d
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()
		cmd := exec.CommandContext(ctx, bwrap, argv...)
		cmd.Dir = "/"
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "MCPSHKA_TEST_HOST_SENTINEL=synthetic-not-a-secret"}
		cmd.ExtraFiles = []*os.File{workspace}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = 3 * time.Second
		var output boundedOutput
		cmd.Stdout = &output
		cmd.Stderr = &output
		err = cmd.Run()
		out := string(output.data)
		if failure != "" {
			if err == nil || !strings.Contains(out, failure) {
				t.Fatalf("fault not detected by intended assertion: %v\n%s", err, out)
			}
		} else if profile == "project-test-v1" {
			if err != nil || !strings.Contains(out, "ok  \tmcpshka.sandbox/fixture/internal/probe\t") || strings.Contains(out, "(cached)") || strings.Contains(out, "[no test files]") {
				t.Fatalf("real Go sandbox failed: %v\n%s", err, out)
			}
			t.Log("unchanged go test executed all four nested workload probes")
		} else if err != nil || !strings.Contains(out, "guest-probe-ok") {
			t.Fatalf("real sandbox failed: %v\n%s", err, out)
		}
		got, readErr := os.ReadFile(outside)
		if readErr != nil || string(got) != "host unchanged" {
			t.Fatal("host canary changed", readErr)
		}
		got, readErr = os.ReadFile(filepath.Join(work, "input"))
		if readErr != nil || string(got) != "workspace input" {
			t.Fatal("source input changed", readErr)
		}
		current, err := os.Stat(filepath.Join(work, "input"))
		if err != nil || current.Mode().Perm() != 0600 {
			t.Fatal("source mode changed", err)
		}
		if failure == "" {
			changed, err := os.ReadFile(filepath.Join(work, "changed"))
			if profile == "project-test-v1" {
				if !os.IsNotExist(err) {
					t.Fatal("read-only source was writable", err)
				}
			} else if err != nil || string(changed) != "private write" {
				t.Fatal("intended private write absent", err)
			}
		}
	}
	for _, mode := range []string{"python", "bash", "make", "npm"} {
		t.Run("isolated-v1/"+mode, func(t *testing.T) { run(t, "isolated-v1", mode, "") })
	}
	t.Run("project-test-v1/go-test", func(t *testing.T) { run(t, "project-test-v1", "", "") })
	for _, fault := range []string{"host_mount", "host_network", "host_proc"} {
		t.Run("fault/"+fault, func(t *testing.T) { run(t, "isolated-v1", "python", fault) })
	}
}

// boundedOutput keeps at most 8 KiB of a sandbox run's output.
type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 8192 {
		return 0, errors.New("executor evidence budget exceeded")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
