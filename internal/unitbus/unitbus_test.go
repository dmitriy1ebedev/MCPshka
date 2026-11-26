package unitbus

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A reply built by hand decodes to the same values; containers, variants
// and padding follow the specification's alignment rules.
func TestWireRoundTrip(t *testing.T) {
	e := &encoder{}
	e.str("")
	msg := call(7, "org.freedesktop.systemd1", "/org/freedesktop/systemd1/unit/ssh_2eservice", "org.freedesktop.DBus.Properties", "GetAll", "s", e.b)
	if msg[0] != 'l' || msg[1] != msgCall || binary.LittleEndian.Uint32(msg[8:]) != 7 {
		t.Fatalf("prefix %v", msg[:16])
	}
	h, err := parse(msg[:16], msg[16:])
	if err != nil || h.sig != "s" || len(h.body) != 5 || (len(msg)-5)%8 != 0 {
		t.Fatalf("parse %+v %v", h, err)
	}
	if v, err := (&decoder{b: h.body}).value("s"); err != nil || v.(string) != "" {
		t.Fatalf("body %v %v", v, err)
	}

	// a{sv} with a string, a bool, a uint64, an empty string array and
	// an ay, as systemd sends them.
	b := &encoder{}
	b.uint32(0)
	at := len(b.b) - 4
	b.align(8)
	start := len(b.b)
	entry := func(name, sig string, put func()) {
		b.align(8)
		b.str(name)
		b.sig(sig)
		put()
	}
	entry("Id", "s", func() { b.str("ssh.service") })
	entry("Transient", "b", func() { b.uint32(1) })
	entry("RuntimeMaxUSec", "t", func() { b.align(8); b.b = binary.LittleEndian.AppendUint64(b.b, infinity) })
	entry("Requires", "as", func() { b.strs(nil) })
	entry("InvocationID", "ay", func() { b.uint32(2); b.b = append(b.b, 0xab, 0xcd) })
	binary.LittleEndian.PutUint32(b.b[at:], uint32(len(b.b)-start))
	v, err := (&decoder{b: b.b}).value("a{sv}")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Variant{}
	for _, kv := range v.([]any) {
		p := kv.([]any)
		got[p[0].(string)] = p[1].(Variant)
	}
	if got["Id"].Value != "ssh.service" || got["Transient"].Value != true || got["RuntimeMaxUSec"].Value != infinity ||
		len(got["Requires"].Value.([]any)) != 0 || !bytes.Equal(got["InvocationID"].Value.([]byte), []byte{0xab, 0xcd}) {
		t.Fatalf("decoded %#v", got)
	}

	// Corrupt input is refused, never panics.
	for _, bad := range [][]byte{b.b[:len(b.b)-3], {0xff, 0xff, 0xff, 0x7f}, append([]byte{1, 0, 0, 0, 's'}, 0, 0, 0)} {
		if _, err := (&decoder{b: bad}).value("a{sv}"); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	for _, sig := range []string{"", "(", "a", "{s}", "()", "(s}", "z"} {
		if _, _, err := single(sig); err == nil {
			t.Fatalf("signature %q accepted", sig)
		}
	}
}

func TestTimespanMatchesSystemd(t *testing.T) {
	for in, want := range map[uint64]string{
		0:                 "0",
		infinity:          "infinity",
		90 * 1000000:      "1min 30s",
		100 * 1000:        "100ms",
		1500 * 1000:       "1.500000s",
		1500:              "1.500ms",
		30 * 60 * 1000000: "30min",
		86400 * 1000000:   "1d",
		3723 * 1000000:    "1h 2min 3s",
	} {
		if got := timespan(in); got != want {
			t.Errorf("timespan(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestUnitPathAndArgs(t *testing.T) {
	if p := unitPath("mcpshka-executor-1a.service"); p != "/org/freedesktop/systemd1/unit/mcpshka_2dexecutor_2d1a_2eservice" {
		t.Fatal(p)
	}
	if p := unitPath("0x.service"); !strings.HasSuffix(p, "/_30x_2eservice") {
		t.Fatal(p)
	}
	q, ok := parseArgs([]string{"show", "--no-pager", "--property=Id,ActiveState", "-p", "MainPID", "--value", "--", "a.service"})
	if !ok || !q.value || strings.Join(q.props, ",") != "Id,ActiveState,MainPID" || q.units[0] != "a.service" {
		t.Fatalf("%+v %v", q, ok)
	}
	for _, args := range [][]string{
		{"show", "a.service"},
		{"is-active", "a.service"},
		{"show", "-p", "Id", "--timestamp=unix", "a.service"},
		{"show", "-p", "Id", "ssh"},
		{"show", "-p", "Id,", "a.service"},
		{"show", "-p"},
	} {
		if _, ok := parseArgs(args); ok {
			t.Fatalf("accepted %v", args)
		}
	}
}

// Timestamps and unknown types are never guessed: the query goes to
// systemctl.
func TestFormatRefusesWhatItCannotSpell(t *testing.T) {
	for name, v := range map[string]Variant{
		"ActiveEnterTimestamp": {Sig: "t", Value: uint64(1)},
		"CPUUsageNSec":         {Sig: "t", Value: uint64(1)},
		"UMask":                {Sig: "u", Value: uint32(18)},
		"ExecStart":            {Sig: "a(sasbttttuii)", Value: []any{}},
		"Environment":          {Sig: "as", Value: []any{"A=b c"}},
		"Requires":             {Sig: "as", Value: []any{`system-systemd\x2dfsck.slice`}},
	} {
		if _, err := format(name, v); err == nil {
			t.Errorf("%s formatted", name)
		}
	}
}

// On a live host the bus answer is byte-for-byte systemctl's.
func TestShowMatchesSystemctl(t *testing.T) {
	if _, err := os.Stat("/usr/bin/systemctl"); err != nil {
		t.Skip("no systemctl")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, ok := Show(ctx, "show", "-p", "Id", "dbus.service"); !ok {
		t.Skip("no reachable D-Bus here")
	}
	props := "--property=Id,LoadState,ActiveState,SubState,UnitFileState,MainPID,NRestarts,ExecMainStatus,ExecMainCode,Result,StatusText,ExecMainStartTimestampMonotonic,RuntimeMaxUSec,TimeoutStartUSec,TimeoutStopUSec,RestartUSec,WatchdogUSec,InvocationID,ControlGroup,Restart,KillMode,Requires,Wants,After,Before"
	for _, args := range [][]string{
		{"show", "--no-pager", props, "dbus.service", "systemd-journald.service"},
		{"show", "--no-pager", props, "mcpshka-unitbus-absent.service"},
		{"show", "--property=Id,LoadState,ActiveState,SubState,MainPID,ExecMainStartTimestampMonotonic,RuntimeMaxUSec,TimeoutStartUSec,InvocationID,KillMode", "systemd-*.service"},
		{"show", "--property=ActiveState,Result", "--value", "systemd-journald.service"},
		{"show", "--property=ActiveState", "--value", "dbus.service", "systemd-journald.service"},
		{"show", "--property=LoadState", "--property=ActiveState", "--property=ControlGroup", "--", "init.scope"},
	} {
		// Live values (StatusText of a busy daemon) may move between the
		// two reads; three tries, then show the first line that differs.
		var diff string
		for try := 0; try < 3; try++ {
			got, ok := Show(ctx, args...)
			if !ok {
				t.Fatalf("bus refused %v", args)
			}
			want, err := exec.CommandContext(ctx, "/usr/bin/systemctl", args...).Output()
			if err != nil {
				t.Fatalf("systemctl %v: %v", args, err)
			}
			if diff = firstDiff(got, want); diff == "" {
				break
			}
		}
		if diff != "" {
			t.Errorf("%v: %s", args, diff)
		}
	}
}

func firstDiff(got, want []byte) string {
	g, w := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			return fmt.Sprintf("line %d: bus %q, systemctl %q (bus %d lines, systemctl %d)", i+1, a, b, len(g), len(w))
		}
	}
	return ""
}
