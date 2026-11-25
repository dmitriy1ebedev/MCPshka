package policy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtendEnv(t *testing.T) {
	base := []string{"PATH=/bin", "HOME=/tmp"}
	got, err := ExtendEnv(base, map[string]string{"Z_TEST": "ok", "A_TEST": "value=2"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "HOME=/tmp", "A_TEST=value=2", "Z_TEST=ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(base) != 2 {
		t.Fatal("mutated baseline")
	}
	for _, additions := range []map[string]string{
		{"PATH": "/unsafe"}, {"9BAD": "x"}, {"BAD=NAME": "x"},
		{"GOOD": "line\nfeed"}, {"GOOD": "nul\x00byte"},
	} {
		if _, err := ExtendEnv(base, additions); err == nil {
			t.Fatalf("accepted %q", additions)
		}
	}
	if _, err := ExtendEnv([]string{"BROKEN"}, nil); err == nil {
		t.Fatal("accepted invalid baseline")
	}
}

func TestRoleProfilesAndPerProjectPolicy(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	child := filepath.Join(locked, "child")
	open := filepath.Join(root, "open")
	for _, dir := range []string{locked, child, open} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}

	p := Default()
	p.AllowedWorkdirs = []string{root}
	if err := p.SetProjectProfile(locked, ProfileReader); err != nil {
		t.Fatal(err)
	}
	if err := p.SetProjectProfile(child, ProfileOperator); err != nil {
		t.Fatal(err)
	}

	decision, err := p.DecideCommandForWorkdir("git", []string{"commit", "-m", "x"}, locked)
	if err == nil {
		t.Fatal("reader profile accepted mutating command")
	}
	if decision.Profile != ProfileReader || decision.Class != ClassMutating || decision.Allowed {
		t.Fatalf("unexpected reader decision: %+v", decision)
	}

	decision, err = p.DecideCommandForWorkdir("git", []string{"status"}, locked)
	if err != nil || !decision.Allowed || decision.Profile != ProfileReader {
		t.Fatalf("reader rejected read-only command: decision=%+v err=%v", decision, err)
	}

	decision, err = p.DecideCommandForWorkdir("git", []string{"commit", "-m", "x"}, child)
	if err != nil || !decision.Allowed || decision.Profile != ProfileOperator {
		t.Fatalf("most-specific project profile not applied: decision=%+v err=%v", decision, err)
	}

	decision, err = p.DecideCommandForWorkdir("git", []string{"commit", "-m", "x"}, open)
	if err != nil || !decision.Allowed || decision.Profile != ProfileOperator {
		t.Fatalf("default operator profile rejected command: decision=%+v err=%v", decision, err)
	}

	if err := p.SetDefaultProfile("missing"); err == nil {
		t.Fatal("accepted unknown default profile")
	}
	if err := p.SetProjectProfile(open, "missing"); err == nil {
		t.Fatal("accepted unknown project profile")
	}
}

func TestRoleProfileCannotBypassGlobalClassCeiling(t *testing.T) {
	root := t.TempDir()
	p := Default()
	p.AllowedWorkdirs = []string{root}
	p.Profiles["unsafe"] = RoleProfile{AllowedCommandClasses: map[CommandClass]bool{
		ClassReadOnly: true, ClassMutating: true, ClassPrivileged: true, ClassDestructive: true,
	}}
	if err := p.SetProjectProfile(root, "unsafe"); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		command string
		args    []string
	}{
		{command: "systemctl", args: []string{"restart", "ssh"}},
		{command: "rm", args: []string{"-rf", "x"}},
	} {
		decision, err := p.DecideCommandForWorkdir(tt.command, tt.args, root)
		if err == nil || decision.Allowed {
			t.Fatalf("global class ceiling bypassed by profile: %+v", decision)
		}
	}
}

func TestWorkdirContainmentAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "project")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	p := &Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}

	got, err := p.CheckWorkdir(inside)
	if err != nil {
		t.Fatal(err)
	}
	if got != inside {
		t.Fatalf("got %q want %q", got, inside)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CheckWorkdir(link); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestDefaultDeniedCommand(t *testing.T) {
	p := Default()
	for _, cmd := range []string{"reboot", "/usr/sbin/reboot", "mkfs.ext4"} {
		if err := p.CheckCommand(cmd); err == nil {
			t.Fatalf("expected %q to be denied", cmd)
		}
	}
}

func TestDefaultCommandClassesAndEnforcement(t *testing.T) {
	p := Default()
	tests := []struct {
		name    string
		command string
		args    []string
		class   CommandClass
		allowed bool
	}{
		{name: "ls", command: "ls", class: ClassReadOnly, allowed: true},
		{name: "git status", command: "git", args: []string{"status", "--short"}, class: ClassReadOnly, allowed: true},
		{name: "git commit", command: "git", args: []string{"commit", "-m", "x"}, class: ClassMutating, allowed: true},
		{name: "git hard reset", command: "git", args: []string{"reset", "--hard", "HEAD"}, class: ClassDestructive, allowed: false},
		{name: "rm", command: "/bin/rm", args: []string{"-rf", "x"}, class: ClassDestructive, allowed: false},
		{name: "sudo", command: "sudo", args: []string{"id"}, class: ClassPrivileged, allowed: false},
		{name: "systemctl status", command: "systemctl", args: []string{"status", "ssh"}, class: ClassReadOnly, allowed: true},
		{name: "systemctl restart", command: "systemctl", args: []string{"restart", "ssh"}, class: ClassPrivileged, allowed: false},
		{name: "systemd-run", command: "systemd-run", args: []string{"--wait", "/usr/bin/true"}, class: ClassPrivileged, allowed: false},
		{name: "ip direct network", command: "ip", args: []string{"route", "add", "default", "via", "192.168.0.1"}, class: ClassPrivileged, allowed: false},
		{name: "tailscale direct network", command: "tailscale", args: []string{"status", "--json"}, class: ClassPrivileged, allowed: false},
		{name: "wireguard direct network", command: "wg", args: []string{"show"}, class: ClassPrivileged, allowed: false},
		{name: "nmcli direct network", command: "nmcli", args: []string{"connection", "show"}, class: ClassPrivileged, allowed: false},
		{name: "unknown executable", command: "my-project-tool", class: ClassMutating, allowed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.ClassifyCommand(tt.command, tt.args); got != tt.class {
				t.Fatalf("class=%q want=%q", got, tt.class)
			}
			decision, err := p.DecideCommand(tt.command, tt.args)
			if decision.Class != tt.class || decision.Allowed != tt.allowed {
				t.Fatalf("decision=%+v want class=%q allowed=%v", decision, tt.class, tt.allowed)
			}
			if tt.allowed && err != nil {
				t.Fatalf("allowed command rejected: %v", err)
			}
			if !tt.allowed && err == nil {
				t.Fatal("disallowed command accepted")
			}
		})
	}
}

func TestLegacyCustomPolicyWithoutClassMapDoesNotImplicitlyDeny(t *testing.T) {
	p := &Policy{DeniedCommands: map[string]struct{}{}}
	decision, err := p.DecideCommand("rm", []string{"file"})
	if err != nil {
		t.Fatalf("legacy custom policy rejected command: %v", err)
	}
	if !decision.Allowed || decision.Class != ClassDestructive {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestTransitiveCommandEffectsAreNotReadOnly(t *testing.T) {
	for _, tt := range []struct {
		name, command string
		args          []string
		class         CommandClass
	}{
		{"find print", "find", []string{".", "-type", "f", "-print"}, ClassReadOnly},
		{"find exec", "find", []string{".", "-exec", "sh", "-c", "echo probe", ";"}, ClassPrivileged},
		{"find execdir", "/usr/bin/find", []string{".", "-execdir", "true", "+"}, ClassPrivileged},
		{"find ok", "find", []string{".", "-ok", "true", ";"}, ClassPrivileged},
		{"find okdir", "find", []string{".", "-okdir", "true", ";"}, ClassPrivileged},
		{"find fprint", "find", []string{".", "-fprint", "output"}, ClassMutating},
		{"find fprint0", "find", []string{".", "-fprint0", "output"}, ClassMutating},
		{"find fprintf", "find", []string{".", "-fprintf", "output", "%p"}, ClassMutating},
		{"find fls", "find", []string{".", "-fls", "output"}, ClassMutating},
		{"find literal delete", "find", []string{".", "-name", "-delete", "-print"}, ClassReadOnly},
		{"find literal exec", "find", []string{".", "-path", "-exec", "-print"}, ClassReadOnly},
		{"find literal output", "find", []string{".", "-printf", "-delete"}, ClassReadOnly},
		{"find exec argument", "find", []string{".", "-exec", "echo", "-delete", ";"}, ClassPrivileged},
		{"find output filename", "find", []string{".", "-fprint", "-delete"}, ClassMutating},
		{"find fprintf format", "find", []string{".", "-fprintf", "output", "-delete"}, ClassMutating},
		{"find delete", "find", []string{".", "-delete"}, ClassDestructive},
		{"find delete after exec", "find", []string{".", "-exec", "true", ";", "-delete"}, ClassDestructive},
		{"find delete before exec", "find", []string{".", "-delete", "-exec", "true", ";"}, ClassDestructive},
		{"rg search", "rg", []string{"needle", "."}, ClassReadOnly},
		{"rg end of options", "rg", []string{"--", "--pre", "file"}, ClassReadOnly},
		{"rg literal pattern", "rg", []string{"-e", "--pre", "file"}, ClassReadOnly},
		{"rg long literal pattern", "rg", []string{"--regexp", "--pre=python3", "file"}, ClassReadOnly},
		{"rg clustered literal pattern", "rg", []string{"-ne", "--pre", "file"}, ClassReadOnly},
		{"rg literal glob", "rg", []string{"--glob", "--pre=python3", "needle"}, ClassReadOnly},
		{"rg effect after pattern", "rg", []string{"-e", "needle", "--pre=python3", "file"}, ClassPrivileged},
		{"rg preprocessor", "rg", []string{"--pre", "python3", "needle", "."}, ClassPrivileged},
		{"rg attached preprocessor", "/usr/bin/rg", []string{"--pre=python3", "needle", "."}, ClassPrivileged},
		{"find exec then fprint does not downgrade", "find", []string{".", "-exec", "sh", "-c", "echo x", ";", "-fprint", "output"}, ClassPrivileged},
		{"bash compact c option", "bash", []string{"-lc", "systemctl stop mcpshka"}, ClassPrivileged},
		{"dash inline", "dash", []string{"-c", "rm -rf protected"}, ClassPrivileged},
		{"python module", "python3", []string{"-I", "-B", "-m", "dangerous.module"}, ClassPrivileged},
		{"python versioned inline", "python3.12", []string{"-c", "import os"}, ClassPrivileged},
		{"node eval", "node", []string{"--eval=process.exit()"}, ClassPrivileged},
		{"npx arbitrary script", "npx", []string{"tool"}, ClassPrivileged},
		{"shell substitution", "bash", []string{"-lc", "echo $(touch marker)"}, ClassPrivileged},
		{"python", "python3", []string{"-c", "print(1)"}, ClassPrivileged},
		{"make recipe", "make", []string{"test"}, ClassPrivileged},
		{"npm script", "npm", []string{"test"}, ClassPrivileged},
		{"pip build", "pip", []string{"install", "."}, ClassPrivileged},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := Default()
			if got := p.ClassifyCommand(tt.command, tt.args); got != tt.class {
				t.Fatalf("class=%s want=%s", got, tt.class)
			}
			if err := p.SetDefaultProfile(ProfileReader); err != nil {
				t.Fatal(err)
			}
			decision, err := p.DecideCommand(tt.command, tt.args)
			if tt.class == ClassReadOnly {
				if err != nil || !decision.Allowed {
					t.Fatalf("read-only command rejected: %+v %v", decision, err)
				}
			} else if err == nil || decision.Allowed {
				t.Fatalf("reader admitted transitive effects: %+v", decision)
			}
		})
	}
}

// A safe-looking outer argv is insufficient to grant operator execution when
// it invokes an interpreter, package hook, find -exec, or ripgrep preprocessor.
func TestOperatorRejectsKnownTransitiveExecution(t *testing.T) {
	p := Default()
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"bash", []string{"-lc", "echo ok; rm -rf /protected"}},
		{"python3", []string{"-c", "import os"}},
		{"find", []string{".", "-exec", "sh", "-c", "echo run", ";"}},
		{"rg", []string{"--pre", "sh", "needle", "."}},
		{"make", []string{"test"}},
		{"npm", []string{"run", "test"}},
	} {
		decision, err := p.DecideCommand(tc.command, tc.args)
		if err == nil || decision.Allowed || decision.Class != ClassPrivileged {
			t.Fatalf("operator admitted transitive execution %q %q: %+v err=%v", tc.command, tc.args, decision, err)
		}
	}
}
