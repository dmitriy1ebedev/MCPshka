package diagtraffic

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDiagTrafficValidation(t *testing.T) {
	ok := [][]string{
		{"start", "--client", "192.168.77.40", "--mac", "02:00:00:00:00:46", "--duration", "300", "--root", RunsRoot},
		{"status", "--run", "20261007T200000Z-deadbeef", "--root", RunsRoot, "--json"},
		{"report", "--run", "20261007T200000Z-deadbeef", "--root", RunsRoot, "--json"},
		{"stop", "--run", "20261007T200000Z-deadbeef", "--root", RunsRoot, "--json"},
	}
	for _, args := range ok {
		if err := Validate(args); err != nil {
			t.Fatalf("accepted diag traffic form rejected %v: %v", args, err)
		}
	}
	bad := [][]string{
		{"start", "--client", "8.8.8.8", "--mac", "02:00:00:00:00:46", "--duration", "300", "--root", RunsRoot},
		{"start", "--client", "192.168.77.40", "--mac", "00:00:00:00:00:00", "--duration", "300", "--root", RunsRoot},
		{"start", "--client", "192.168.77.40", "--mac", "02:00:00:00:00:46", "--duration", "301", "--root", RunsRoot},
		{"stop", "--run", "../escape", "--root", RunsRoot, "--json"},
		{"start", "--client", "192.168.77.40", "--mac", "02:00:00:00:00:46", "--duration", "300", "--root", "/tmp"},
		{"status", "--run", "../escape", "--root", RunsRoot, "--json"},
		{"report", "--run", "good-run", "--root", "/tmp", "--json"},
		{"start", "--client", "192.168.77.40"},
	}
	for _, args := range bad {
		if err := Validate(args); err == nil {
			t.Fatalf("unsafe diag traffic form accepted: %v", args)
		}
	}
}

func testRun(t *testing.T) runDir {
	t.Helper()
	fs, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	return runDir{id: "20261126T120000Z-deadbeef", fs: fs}
}

func TestARPAndIPv6Identity(t *testing.T) {
	const client = "192.168.77.50"
	const mac = "aa:bb:cc:dd:ee:ff"
	raw := "IP address HW type Flags HW address Mask Device\n" +
		client + " 0x1 0x0 00:00:00:00:00:00 * eth0\n" +
		client + " 0x1 0x2 11:22:33:44:55:66 * eth0\n"
	if got := arpMAC(strings.NewReader(raw), client); got != "11:22:33:44:55:66" {
		t.Fatal("incomplete ARP hid the actual MAC", got)
	}
	raw = "fe80::1 dev eth0 lladdr " + mac + " STALE\n2001:db8::2 dev eth0 lladdr " + mac + " REACHABLE\n" +
		"2001:db8::3 dev eth0 lladdr 11:22:33:44:55:66 STALE\n2001:db8::2 dev eth1 lladdr " + mac + " STALE\n"
	if got := stringsOfIPs(neighborAddresses([]byte(raw), mac)); !slices.Equal(got, []string{"2001:db8::2", "fe80::1"}) {
		t.Fatal("IPv6 identity binding", got)
	}
}

func TestReportKeepsIPv4IPv6QUICAndMarks(t *testing.T) {
	d := testRun(t)
	text := "2026-11-26 00:00:00 eth0 In IP 192.168.77.50.53000 > 192.168.0.1.53: UDP, 10 A? Example.org.\n" +
		"2026-11-26 00:00:01 wg0 Out x ethertype IPv6 (0x86dd), length 100:\n" +
		"    2001:db8::2.50000 > 2001:db8::9.443: UDP, length 80\n" +
		"2026-11-26 00:00:02 eth0 In IP 192.168.0.60.53000 > 192.168.0.1.53: UDP, 10 A? unrelated.org.\n"
	if err := d.writeFile("pcap_text_0.txt", []byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := d.writeFile("conntrack.events.log", []byte("src=192.168.77.50 mark=0x10\n")); err != nil {
		t.Fatal(err)
	}
	r := d.summarize(netip.MustParseAddr("192.168.77.50"), []netip.Addr{netip.MustParseAddr("2001:db8::2")}, []pcapFile{})
	if r.Packets != 2 || r.IPv4Packets != 1 || r.IPv6Packets != 1 || r.QUIC != 1 ||
		!slices.Equal(r.Interfaces, []count{{"eth0", 1}, {"wg0", 1}}) ||
		!slices.Equal(r.DNS, []count{{"example.org", 1}}) || !slices.Equal(r.Marks, []count{{"0x10", 1}}) {
		t.Fatalf("unexpected report %+v", r)
	}
	if err := d.writeReport(r); err != nil {
		t.Fatal(err)
	}
	raw, err := d.readFile("report.json", stateLimit)
	if err != nil || !bytes.Contains(raw, []byte(`"schemaVersion": "diag.traffic-route/v1alpha1"`)) {
		t.Fatalf("invalid written report %s, %v", raw, err)
	}
}

func TestKernelFileLimitStopsCollector(t *testing.T) {
	d := testRun(t)
	f, err := d.create("capture")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const limit = 4096
	code, err := collect(t.Context(), []string{"/usr/bin/yes"}, nil, f, limit, 2*time.Second)
	if err != nil || code == 0 {
		t.Fatalf("unbounded collector: code=%d %v", code, err)
	}
	info, err := f.Stat()
	if err != nil || info.Size() != limit {
		t.Fatalf("hard cap: %v %v", info, err)
	}
}

func TestStopIsIdempotentAndDoesNotSignalUnrelatedPID(t *testing.T) {
	d := testRun(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := exec.CommandContext(ctx, "/usr/bin/sleep", "10")
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { p.Process.Kill(); p.Wait() }()
	if err := d.save(state{"state": "RUNNING", "pid": p.Process.Pid, "observer_pids": map[string]int{"tcpdump": p.Process.Pid}}); err != nil {
		t.Fatal(err)
	}
	s, err := d.stop(ctx, 0)
	if err != nil || s["state"] != "CANCELLED" || s["ended_by"] != "forced_stop" {
		t.Fatalf("missing stop receipt: %+v %v", s, err)
	}
	if err := p.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("stop signalled an unrelated process", err)
	}
	finished := s["finished_at"]
	s, err = d.stop(ctx, 0)
	if err != nil || s["finished_at"] != finished {
		t.Fatalf("stop changed terminal receipt: %+v %v", s, err)
	}
}

func TestRunFilesRejectSymlinks(t *testing.T) {
	d := testRun(t)
	outside := filepath.Join(t.TempDir(), "previous.json")
	if err := os.WriteFile(outside, []byte("other data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.fs.Symlink(outside, "state.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.load(); err == nil {
		t.Fatal("followed a state symlink")
	}
}
