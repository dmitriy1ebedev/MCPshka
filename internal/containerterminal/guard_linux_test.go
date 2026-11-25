package containerterminal

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestProcessStatIdentityAndResources(t *testing.T) {
	f := strings.Fields("S 123 0 0 0 0 0 0 0 0 0 20 30 0 0 0 0 0 0 456 1000 2")
	p, e := parseStat(321, fmt.Sprintf("321 (name with ) spaces) %s", strings.Join(f, " ")))
	if e != nil || p.pid != 321 || p.parent != 123 || p.start != 456 || p.cpu != 50 || p.rss <= 0 {
		t.Fatalf("%+v %v", p, e)
	}
	for _, bad := range []string{"", "321 no delimiter", "321 (x) S", "321 (x) " + strings.ReplaceAll(strings.Join(f, " "), "456", "invalid")} {
		if _, e := parseStat(321, bad); e == nil {
			t.Fatal("invalid process identity admitted")
		}
	}
}

func TestOutputQuotaBeforeForwarding(t *testing.T) {
	var b bytes.Buffer
	var count atomic.Uint64
	count.Store((1 << 20) - 3)
	w := countingWriter{&b, &count}
	n, e := w.Write([]byte("abcdef"))
	if n != 3 || e == nil || b.String() != "abc" || count.Load() != 1<<20 {
		t.Fatalf("quota: %d %v %q", n, e, b.String())
	}
	if n, e = w.Write([]byte("x")); n != 0 || e == nil {
		t.Fatal("output exceeded hard quota")
	}
}

func TestFenceChecksBirthIdentityAndStopsOwnedChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
	if e != nil {
		t.Fatal(e)
	}
	p, e := parseStat(cmd.Process.Pid, string(b))
	if e != nil {
		t.Fatal(e)
	}
	wrong := p
	wrong.start++
	if e = fence([]process{wrong}); e == nil {
		t.Fatal("replacement birth admitted")
	}
	if e = cmd.Process.Signal(os.Signal(syscall.Signal(0))); e != nil {
		t.Fatal("identity mismatch killed child")
	}
	ps, e := descendants(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range ps {
		if v.pid == p.pid && v.start == p.start {
			found = true
		}
	}
	if !found {
		t.Fatal("owned child missing")
	}
	if e = fence([]process{p}); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b, e = os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.pid))
		if e != nil || strings.Contains(string(b), ") Z ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fenced child still executing")
}
