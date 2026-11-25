// Package motd tests the H96 SSH login summary scripts (MCP-173): the login
// hook only prints a bounded, control-free snapshot and never waits for the
// collectors.
package motd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "sh", "sed", "awk", "timeout", "flock", "iconv", "fold"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	if out, _ := exec.Command("sed", "--version").Output(); !bytes.Contains(out, []byte("GNU")) {
		t.Skip("the scripts target GNU sed (Debian on H96)")
	}
}

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// run executes a repository script with the given environment and fails when
// it takes longer than limit: neither script may hang a login.
func run(t *testing.T, limit time.Duration, env []string, argv ...string) []byte {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	start := time.Now()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", argv, err)
	}
	if took := time.Since(start); took > limit {
		t.Fatalf("%v took %s, limit %s", argv, took, limit)
	}
	return out
}

// assertSafe is the contract of everything an SSH client receives at login.
func assertSafe(t *testing.T, out []byte, maxBytes int) {
	t.Helper()
	if len(out) > maxBytes {
		t.Fatalf("output is %d bytes, limit %d", len(out), maxBytes)
	}
	if !utf8.Valid(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
	for _, r := range string(out) {
		ascii := r == '\n' || (r >= 0x20 && r < 0x7f)
		cyrillic := r >= 0x400 && r <= 0x47f
		if !ascii && !cyrillic {
			t.Fatalf("unsafe character %U in %q", r, out)
		}
	}
}

func refresh(t *testing.T, dashboard string, limit time.Duration, env ...string) []byte {
	t.Helper()
	snap := filepath.Join(t.TempDir(), "snapshot")
	render, err := filepath.Abs("router-motd-compact.awk")
	if err != nil {
		t.Fatal(err)
	}
	env = append(env, "ROUTER_MOTD_DASHBOARD="+dashboard, "ROUTER_MOTD_SNAPSHOT="+snap, "ROUTER_MOTD_RENDER="+render)
	run(t, limit, env, "bash", "router-motd-refresh", "--force")
	out, err := os.ReadFile(snap + ".plain")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSnapshotHasNoTerminalControl(t *testing.T) {
	needTools(t)
	dash := script(t, t.TempDir(), "dash", "#!/bin/bash\n"+
		`printf '\e[0;36mH96\e[0m \e[0;90m│\e[0m \e[0;92m✓\e[0m диск 74%% · 62°C\n'`+"\n"+
		`printf '\e]0;title\a\e]52;c;AAAA\e\\ \e[?1049h\e[2J\e[H ⚠ память — мало 🔥\r\n'`+"\n"+
		`printf 'bell\a nul\0 del\177 tab\there \eM lone-esc \e\n'`+"\n")
	out := refresh(t, dash, 10*time.Second)
	assertSafe(t, out, 2100)
	for _, want := range []string{"H96", "FIX", "more: motd"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("snapshot lost %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{"title", "AAAA", "[0;", "1049", "[2J"} {
		if strings.Contains(string(out), leak) {
			t.Fatalf("escape sequence payload %q leaked:\n%s", leak, out)
		}
	}
}

func TestSnapshotIsBounded(t *testing.T) {
	needTools(t)
	dash := script(t, t.TempDir(), "dash", "#!/bin/bash\n"+
		`for i in $(seq 40); do printf 'строка %s %s\n' "$i" "$(head -c 1000 /dev/zero | tr '\0' 'x')"; done`+"\n")
	out := refresh(t, dash, 10*time.Second)
	assertSafe(t, out, 2100)
	if !strings.Contains(string(out), "data unavailable") {
		t.Fatalf("malformed dashboard must produce bounded fallback:\n%s", out)
	}
}

func TestSnapshotSurvivesStuckAndFailingCollectors(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"stuck":   "#!/bin/bash\necho partial\nsleep 60\n",
		"failing": "#!/bin/bash\necho partial\nexit 3\n",
		"silent":  "#!/bin/bash\nexit 0\n",
		"flood":   "#!/bin/bash\necho partial\nhead -c 4000000 /dev/zero\n",
	} {
		out := refresh(t, script(t, dir, name, body), 8*time.Second, "ROUTER_MOTD_TIMEOUT=1")
		assertSafe(t, out, 300)
		if !strings.Contains(string(out), "data unavailable") || strings.Contains(string(out), "partial") {
			t.Fatalf("%s: want the fallback line only, got:\n%s", name, out)
		}
	}
	missing := refresh(t, filepath.Join(dir, "absent"), 8*time.Second)
	if !strings.Contains(string(missing), "data unavailable") {
		t.Fatalf("missing dashboard: %s", missing)
	}
}

func TestLoginPrintsSnapshotWithoutWaiting(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	snap := filepath.Join(dir, "snapshot")
	marker := filepath.Join(dir, "refresh-started")
	// The refresh is slow and noisy; login must not launch it at all.
	slow := script(t, dir, "slow", "#!/bin/sh\necho started >"+marker+"\necho NOISE\nsleep 60\n")
	env := []string{"ROUTER_MOTD_SNAPSHOT=" + snap, "ROUTER_MOTD_REFRESH=" + slow, "ROUTER_MOTD_RUNNER=", "ROUTER_MOTD_COLOR=always", "ROUTER_MOTD_OFF=" + filepath.Join(dir, "off")}

	if out := run(t, 2*time.Second, env, "sh", "router-motd-login"); len(out) != 0 {
		t.Fatalf("no snapshot yet, want empty output, got %q", out)
	}
	// A snapshot tampered with after it was written is still filtered and capped.
	dirty := "\n H96  up 2d14h  load 2.2  temp 67C\nFIX  cool CPU 67C\nmore: motd\n"
	if err := os.WriteFile(snap, []byte(dirty), 0o644); err != nil {
		t.Fatal(err)
	}
	out := run(t, 2*time.Second, env, "sh", "router-motd-login")
	if len(out) > 1024 || bytes.ContainsAny(out, "\x1b\x07\x00\r") || bytes.Contains(out, []byte("NOISE")) {
		t.Fatalf("login output is not bounded and control-free: %d bytes %q", len(out), out[:80])
	}
	if !bytes.Contains(out, []byte(" H96  up ")) {
		raw, _ := os.ReadFile(snap)
		t.Fatalf("login lost snapshot: input=%q output=%q TERM=%q COLOR=%q", raw[:min(80, len(raw))], out[:min(80, len(out))], os.Getenv("TERM"), os.Getenv("ROUTER_MOTD_COLOR"))
	}
	// Check that login did not spawn the collector asynchronously.
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("login started background collection; all refreshes must be timer-driven")
	}

	// Invalid refresh paths and the off switch never fail the login.
	broken := append([]string{}, env...)
	broken[1] = "ROUTER_MOTD_REFRESH=" + filepath.Join(dir, "absent")
	run(t, 2*time.Second, broken, "sh", "router-motd-login")
	if err := os.WriteFile(filepath.Join(dir, "off"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(t, 2*time.Second, env, "sh", "router-motd-login"); len(out) != 0 {
		t.Fatalf("off switch: want no output, got %q", out)
	}
}

func TestSnapshotFitsMobile(t *testing.T) {
	needTools(t)
	dash := script(t, t.TempDir(), "dash", "#!/bin/sh\n"+
		"printf '%s\\n' '"+strings.Repeat("Состояние Lane MCPshka ", 16)+"'\n")
	out := refresh(t, dash, 10*time.Second)
	assertSafe(t, out, 2100)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if w := utf8.RuneCountInString(line); w > 48 {
			t.Fatalf("mobile line width %d > 48: %q", w, line)
		}
	}
}

func TestInstallBacksUpAndRollsBack(t *testing.T) {
	needTools(t)
	root := t.TempDir()
	hook := filepath.Join(root, "etc/update-motd.d/32-router-dashboard")
	motd := filepath.Join(root, "usr/local/bin/motd")
	for _, d := range []string{filepath.Dir(hook), filepath.Dir(motd), filepath.Join(root, "root")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	original := "#!/bin/bash\nprintf '\\e[0;36mH96\\e[0m │ ✓ всё хорошо\\n'\n"
	wrapper := "#!/bin/bash\nexec /etc/update-motd.d/32-router-dashboard \"$@\"\n"
	script(t, filepath.Dir(hook), filepath.Base(hook), original)
	script(t, filepath.Dir(motd), "motd", wrapper)
	env := []string{"ROOT=" + root}

	for i := 0; i < 2; i++ { // a repeated apply must not overwrite the backup of the original
		out := run(t, 20*time.Second, env, "bash", "install.sh", "apply")
		if !bytes.Contains(out, []byte("H96")) {
			t.Fatalf("apply %d did not show the safe login text:\n%s", i, out)
		}
	}
	if got, _ := os.ReadFile(hook); !bytes.Contains(got, []byte("router-motd-login (MCP-173)")) {
		t.Fatalf("login hook was not replaced:\n%s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "usr/local/libexec/router-dashboard")); string(got) != original {
		t.Fatalf("rich dashboard was not preserved:\n%s", got)
	}
	if got, _ := os.ReadFile(motd); !bytes.Contains(got, []byte("exec /usr/local/libexec/router-dashboard")) {
		t.Fatalf("motd command does not point at the rich dashboard:\n%s", got)
	}

	run(t, 20*time.Second, env, "bash", "install.sh", "rollback")
	if got, _ := os.ReadFile(hook); string(got) != original {
		t.Fatalf("rollback did not restore the original hook:\n%s", got)
	}
	if got, _ := os.ReadFile(motd); string(got) != wrapper {
		t.Fatalf("rollback did not restore motd:\n%s", got)
	}
}

func TestV2StageInstallAndRollback(t *testing.T) {
	needTools(t)
	root := t.TempDir()
	hook := filepath.Join(root, "etc/update-motd.d/32-router-dashboard")
	refresh := filepath.Join(root, "usr/local/libexec/router-motd-refresh")
	service := filepath.Join(root, "etc/systemd/system/router-motd-refresh.service")
	timer := filepath.Join(root, "etc/systemd/system/router-motd-refresh.timer")
	for _, path := range []string{hook, refresh} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldHook := "#!/bin/sh\n# router-motd-login (MCP-173)\necho OLD\n"
	oldRefresh := "#!/bin/sh\necho OLD-REFRESH\n"
	script(t, filepath.Dir(hook), filepath.Base(hook), oldHook)
	script(t, filepath.Dir(refresh), filepath.Base(refresh), oldRefresh)
	env := []string{"ROOT=" + root}

	run(t, 15*time.Second, env, "bash", "install-v2.sh", "apply")
	if got, _ := os.ReadFile(hook); !bytes.Contains(got, []byte("timer-driven")) && !bytes.Contains(got, []byte("timer")) {
		t.Fatalf("new login hook missing: %s", got)
	}
	for _, unit := range []string{service, timer} {
		info, err := os.Stat(unit)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("unit %s has wrong permissions or does not exist: %v", unit, err)
		}
	}
	if out := run(t, 15*time.Second, env, "bash", "install-v2.sh", "apply"); !bytes.Contains(out, []byte("already installed")) {
		t.Fatalf("repeated apply should be idempotent: %s", out)
	}

	run(t, 15*time.Second, env, "bash", "install-v2.sh", "rollback")
	if got, _ := os.ReadFile(hook); string(got) != oldHook {
		t.Fatalf("rollback did not restore hook: %q", got)
	}
	if got, _ := os.ReadFile(refresh); string(got) != oldRefresh {
		t.Fatalf("rollback did not restore refresh: %q", got)
	}
	for _, unit := range []string{service, timer} {
		if _, err := os.Stat(unit); !os.IsNotExist(err) {
			t.Fatalf("rollback must remove new unit %s: %v", unit, err)
		}
	}
}

// 18.11: under heavy build load the collector timed out and the login showed
// "data unavailable"; a fresh good snapshot must survive one slow refresh.
func TestSlowCollectorKeepsFreshGoodSnapshot(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	snap := filepath.Join(dir, "snapshot")
	good := []byte("\nH96  ok диск 74%\n\n")
	render, err := filepath.Abs("router-motd-compact.awk")
	if err != nil {
		t.Fatal(err)
	}
	stuck := script(t, dir, "stuck", "#!/bin/bash\nsleep 60\n")
	for _, tc := range []struct {
		stale string
		keep  bool
	}{{"20", true}, {"0", false}} {
		for _, p := range []string{snap, snap + ".plain"} {
			if err := os.WriteFile(p, good, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		run(t, 8*time.Second, []string{"ROUTER_MOTD_DASHBOARD=" + stuck, "ROUTER_MOTD_SNAPSHOT=" + snap, "ROUTER_MOTD_RENDER=" + render, "ROUTER_MOTD_TIMEOUT=1", "ROUTER_MOTD_STALE_MIN=" + tc.stale}, "bash", "router-motd-refresh", "--force")
		out, err := os.ReadFile(snap + ".plain")
		if err != nil {
			t.Fatal(err)
		}
		if kept := string(out) == string(good); kept != tc.keep {
			t.Fatalf("stale=%s keep=%v got:\n%s", tc.stale, tc.keep, out)
		}
	}
}
