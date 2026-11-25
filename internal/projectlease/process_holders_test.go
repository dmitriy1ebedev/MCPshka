package projectlease

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestSealedProcessHoldersNamesEveryHolderWithAdvice(t *testing.T) {
	if os.Geteuid() != 0 {
		// Production runs this scan only in the root recovery worker; an
		// unprivileged scan of other users' descriptors fails closed.
		t.Skip("requires root to inspect every process")
	}
	dir := t.TempDir()
	f, err := os.Create(dir + "/held")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command("sleep", "30")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	holders, err := sealedProcessHolders([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]ProcessHolder{}
	for _, h := range holders {
		if _, dup := seen[h.PID]; dup {
			t.Fatalf("pid %d reported twice", h.PID)
		}
		seen[h.PID] = h
	}
	self, ok := seen[os.Getpid()]
	if !ok || !strings.HasPrefix(self.Reference, "fd/") || self.Path != dir+"/held" || self.Recommendation == "" {
		t.Fatalf("open descriptor holder missing: %+v", holders)
	}
	child, ok := seen[cmd.Process.Pid]
	if !ok || child.Reference != "cwd" || !strings.Contains(child.Command, "sleep") {
		t.Fatalf("root cwd holder missing: %+v", holders)
	}
	if err := noSealedProcessReferences([]string{dir}); err == nil || !strings.Contains(err.Error(), "pid ") {
		t.Fatalf("error lacks holder details: %v", err)
	}
}

func TestHolderRecommendationNamesTheBlocker(t *testing.T) {
	for _, tc := range []struct {
		h    ProcessHolder
		want string
	}{
		{ProcessHolder{Command: "claude bg-spare --bg-spare /tmp/x", Reference: "cwd"}, "spare worker"},
		{ProcessHolder{Command: "claude --resume x", Reference: "cwd"}, "sibling worktree"},
		{ProcessHolder{Command: "bash", Reference: "cwd"}, "working directory"},
		{ProcessHolder{Command: "python3 x", Reference: "maps"}, "maps"},
		{ProcessHolder{Command: "vim", Reference: "fd/3"}, "open repository file"},
	} {
		if got := holderRecommendation(tc.h); !strings.Contains(got, tc.want) {
			t.Fatalf("%+v: %q", tc.h, got)
		}
	}
	got := summarizeHolders([]ProcessHolder{{PID: 7, UID: 0, Command: "x", Reference: "cwd", Path: "/r", Recommendation: "stop"}})
	if !strings.Contains(got, "pid 7") || !strings.Contains(got, "cwd=/r") {
		t.Fatal(got)
	}
}

func TestReadOnlyFileDescriptorDoesNotHoldAStaleLease(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/f"
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ro, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	rw, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fd := func(f *os.File) string { return "/proc/self/fd/" + strconv.Itoa(int(f.Fd())) }
	if !readOnlyFileFD(fd(ro)) {
		t.Fatal("read-only file must not hold the lease")
	}
	if readOnlyFileFD(fd(rw)) || readOnlyFileFD(fd(d)) || readOnlyFileFD("/proc/self/cwd") {
		t.Fatal("writable file, directory or cwd must still hold the lease")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !readOnlyFileFD(fd(ro)) {
		t.Fatal("deleted read-only file must not hold the lease")
	}
}

func TestHolderNamesTheSystemService(t *testing.T) {
	if got := systemUnit("0::/system.slice/desktop-commander-remote.service\n"); got != "desktop-commander-remote.service" {
		t.Fatal(got)
	}
	for _, cg := range []string{"0::/user.slice/user-0.slice/session-3.scope", "0::/system.slice/x.service/sub", "1:name=systemd:/system.slice/a.service"} {
		if got := systemUnit(cg); got != "" {
			t.Fatalf("%q: %q", cg, got)
		}
	}
	if got := holderRecommendation(ProcessHolder{Command: "node", Reference: "fd/9", Unit: "dc.service"}); !strings.Contains(got, "service_action restart dc.service") {
		t.Fatal(got)
	}
}
