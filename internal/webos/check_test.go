package webos

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSSH installs a script answering like the TV: without a terminal it
// prints noTTY (stderr when it starts with "!"), with -tt it prints tty.
func fakeSSH(t *testing.T, noTTY, tty string) {
	dir := t.TempDir()
	emit := func(s string) string {
		if strings.HasPrefix(s, "!") {
			return "echo '" + s[1:] + "' >&2; exit 255"
		}
		return "echo '" + s + "'"
	}
	script := "#!/bin/sh\nif [ \"$1\" = -tt ]; then " + emit(tty) + "; else " + emit(noTTY) + "; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := sshBin
	sshBin = filepath.Join(dir, "ssh")
	t.Cleanup(func() { sshBin = old })
}

func TestRunningAppsFallsBackToTerminalAndExplainsFailure(t *testing.T) {
	fakeSSH(t, `{"returnValue":true,"running":[{"id":"io.pip"}]}`, "!unused")
	if out, err := runningApps(t.Context(), "192.0.2.1"); err != nil || !strings.Contains(out, `"io.pip"`) {
		t.Fatalf("no-tty answer: %q %v", out, err)
	}
	fakeSSH(t, "!PTY allocation request failed", `{"returnValue":true,"running":[]}`)
	if out, err := runningApps(t.Context(), "192.0.2.1"); err != nil || !strings.Contains(out, "returnValue") {
		t.Fatalf("tty fallback: %q %v", out, err)
	}
	fakeSSH(t, "!luna-send-pub: not found", "!luna-send-pub: not found")
	if _, err := runningApps(t.Context(), "192.0.2.1"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("TV's reason lost: %v", err)
	}
}

// 27.10: installed PASS, running UNKNOWN "PTY allocation request failed":
// the plain call's outcome must lead, the PTY refusal is only context.
func TestRunningAppsReportsPlainCallBeforePTYRefusal(t *testing.T) {
	fakeSSH(t, "", "!PTY allocation request failed on channel 0")
	_, err := runningApps(t.Context(), "192.0.2.1")
	if err == nil || !strings.HasPrefix(err.Error(), "without terminal: no answer") || !strings.Contains(err.Error(), "refused by the TV jail") {
		t.Fatalf("diagnosis: %v", err)
	}
}

func TestClosedDeveloperPortFails(t *testing.T) {
	r := Test(t.Context(), "127.0.0.9", "io.app", "", time.Millisecond)
	if r.Pass || len(r.Facts) != 1 || r.Facts[0].ID != "developer_mode" || r.Facts[0].State != "FAIL" {
		t.Fatalf("%+v", r)
	}
}

func TestConsumerRateCountsOnlyTheTV(t *testing.T) {
	var sent atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := sent.Add(100000)
		fmt.Fprintf(w, `{"consumers":[{"remote_addr":"192.168.77.40:5000","send":%d},{"remote_addr":"192.168.0.9:1","send":999999}]}`, n)
	}))
	defer srv.Close()
	Go2RTC = srv.URL
	rate, found, err := consumerRate(t.Context(), "pip", "192.168.77.40", 100*time.Millisecond)
	if err != nil || !found || rate != 1000000 {
		t.Fatalf("rate %d found %v err %v", rate, found, err)
	}
	if _, found, _ := consumerRate(t.Context(), "pip", "192.168.77.50", time.Millisecond); found {
		t.Fatal("foreign consumer counted")
	}
}
