package rootbroker

import (
	"strings"
	"testing"
)

func TestProjectAdoptArguments(t *testing.T) {
	s, o, d := strings.Repeat("a", 32), "openai-tunnel", strings.Repeat("b", 64)
	for _, ok := range [][]string{
		{"project-adopt", s, o, "preview", "/root/repos/free-my-tv", "-"},
		{"project-adopt", s, o, "preview", "/root/repos/free-my-tv", "/root/free-my-tv"},
		{"project-adopt", s, o, "apply", "/root/repos/free-my-tv", "-", d},
	} {
		if err := validateProjectAdopt(ok); err != nil {
			t.Fatal(ok, err)
		}
	}
	for _, bad := range [][]string{
		{"project-adopt", s, "local-stdio", "preview", "/root/repos/x", "-"},
		{"project-adopt", s, o, "apply", "/root/repos/x", "-"},
		{"project-adopt", s, o, "preview", "/root/repos/x", "-", d},
		{"project-adopt", s, o, "apply", "/root/repos/x", "-", "short"},
		{"project-adopt", s, o, "preview", "/root/repos/mcpshka-controlled", "-"},
		{"project-adopt", s, o, "preview", "/root/repos/x/y", "-"},
		{"project-adopt", s, o, "preview", "/root/repos/x", "/root/.ssh"},
		{"project-adopt", s, o, "preview", "/root/repos/x", "/etc"},
		{"project-adopt", s, o, "chown", "/root/repos/x", "-"},
	} {
		if validateProjectAdopt(bad) == nil {
			t.Fatal("accepted", bad)
		}
	}
	argv := projectAdoptServiceArgs([]string{"project-adopt", s, o, "apply", "/root/repos/x", "-", d})
	if tail := strings.Join(argv[len(argv)-6:], " "); tail != installedMCPshka+" project-adopt apply /root/repos/x - "+d {
		t.Fatal("unit command", tail)
	}
}

func TestLeaseRecoveryPreviewRefusalIsNotUnknown(t *testing.T) {
	for _, a := range []string{"preview-stale-lease", "preview-legacy-lease", "status-legacy-lease", "preview-interactive-fence"} {
		if !leaseRecoveryReadOnly(a) {
			t.Fatal(a)
		}
	}
	for _, a := range []string{"release-stale-lease", "recover-legacy-lease", "repair-interactive-fence"} {
		if leaseRecoveryReadOnly(a) {
			t.Fatal(a)
		}
	}
}
