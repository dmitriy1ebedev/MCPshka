package rootbroker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateApprovedPlan(t *testing.T) {
	ok := ApprovedPlan{Executable: "/usr/bin/true", Args: []string{"x"}, Workdir: "/", TimeoutSec: 30}
	if err := validateApproved(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*ApprovedPlan){
		"relative exe":    func(p *ApprovedPlan) { p.Executable = "true" },
		"timeout":         func(p *ApprovedPlan) { p.TimeoutSec = 5 },
		"multiline arg":   func(p *ApprovedPlan) { p.Args = []string{"a\nb"} },
		"write root":      func(p *ApprovedPlan) { p.Write = []string{"/"} },
		"write shadow":    func(p *ApprovedPlan) { p.Write = []string{"/etc/shadow"} },
		"write parent":    func(p *ApprovedPlan) { p.Write = []string{"/etc"} },
		"write authority": func(p *ApprovedPlan) { p.Write = []string{"/var/lib/mcpshka-executor/approved"} },
		"device":          func(p *ApprovedPlan) { p.Devices = []string{"/tmp/x"} },
		"unclean path":    func(p *ApprovedPlan) { p.Write = []string{"/opt/../etc"} },
	}
	for name, mutate := range bad {
		p := ok
		mutate(&p)
		if validateApproved(p) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestApprovedPurposeShowsCommandAndBinding(t *testing.T) {
	p := ApprovedPlan{Executable: "/usr/bin/ares-install", Args: []string{"app.ipk"}, Write: []string{"/opt/x"}, Network: true, TimeoutSec: 60}
	h := approvedHash(p)
	s := approvedPurpose(p, h)
	for _, want := range []string{"/usr/bin/ares-install app.ipk", "/opt/x", "сеть: да", h[:12]} {
		if !strings.Contains(s, want) {
			t.Errorf("purpose %q lacks %q", s, want)
		}
	}
	p.SHA256 = "different"
	if approvedHash(p) == h {
		t.Error("plan hash ignores the executable SHA")
	}
}

// The broker runs with ProtectHome=tmpfs: it must not judge /root paths by
// what it can see (the 0.1.191–0.1.195 webos_deploy failure).
func TestValidateApprovedDoesNotStatHome(t *testing.T) {
	p := ApprovedPlan{Executable: "/usr/bin/python3.13", Args: []string{"-I"}, Workdir: "/root/repos/free-my-tv", Inputs: []string{"/root/repos/free-my-tv/go2rtc-webos-pip.ipk"}, Network: true, TimeoutSec: 300}
	if err := validateApproved(p); err != nil {
		t.Fatalf("shape check rejected a valid /root plan: %v", err)
	}
}

func TestInspectApprovedHashesFiles(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "app.ipk")
	os.WriteFile(in, []byte("ipk"), 0o600)
	got := inspectApproved(ApprovedPlan{Executable: "/usr/bin/true", Workdir: dir, Inputs: []string{in}})
	if got.Error != "" {
		t.Skipf("host lacks a root-owned /usr/bin/true: %s", got.Error)
	}
	if got.SHA256 == "" || got.InputSHAs[in] == "" {
		t.Fatalf("%+v", got)
	}
	if bad := inspectApproved(ApprovedPlan{Executable: "/usr/bin/true", Workdir: dir, Inputs: []string{in + ".missing"}}); bad.Error == "" {
		t.Fatal("missing input accepted")
	}
}
