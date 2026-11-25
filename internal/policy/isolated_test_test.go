package policy

import "testing"

func TestIsolatedProjectTestDoesNotEnableNativeMake(t *testing.T) {
	p := Default()
	root := t.TempDir()
	p.AllowedWorkdirs = []string{root}
	if _, err := p.DecideCommandForWorkdir("make", []string{"test"}, root); err == nil {
		t.Fatal("native Make admitted")
	}
	if d, err := p.DecideIsolatedProjectTest("make", []string{"test"}, root); err != nil || !d.Allowed || d.Class != ClassPrivileged {
		t.Fatalf("fixed sandbox denied: %+v %v", d, err)
	}
	for _, args := range [][]string{{"build"}, {"test", "SHELL=/bin/sh"}, {"-f", "/tmp/Makefile", "test"}} {
		if _, err := p.DecideIsolatedProjectTest("make", args, root); err == nil {
			t.Fatal("arbitrary recipe admitted", args)
		}
	}
	if _, err := p.DecideIsolatedProjectTest("/tmp/make", []string{"test"}, root); err == nil {
		t.Fatal("untrusted executable admitted")
	}
	for _, argv := range [][]string{{"sh", "tests/run_safe.sh"}, {"./tests/run_safe.sh"}, {"python3", "-m", "pytest", "-q"}} {
		if d, err := p.DecideIsolatedProjectTest(argv[0], argv[1:], root); err != nil || !d.Allowed {
			t.Fatalf("registered sealed argv denied %v: %v", argv, err)
		}
	}
	for _, command := range []string{"/usr/bin/python3", "./../x", "a/b", ""} {
		if _, err := p.DecideIsolatedProjectTest(command, nil, root); err == nil {
			t.Fatal("unsafe sealed command admitted", command)
		}
	}
	p.DefaultProfile = ProfileReader
	if _, err := p.DecideIsolatedProjectTest("make", []string{"test"}, root); err == nil {
		t.Fatal("reader admitted")
	}
	p.DefaultProfile = ProfileOperator
	p.DeniedCommands["make"] = struct{}{}
	if _, err := p.DecideIsolatedProjectTest("make", []string{"test"}, root); err == nil {
		t.Fatal("explicit deny bypassed")
	}
}
