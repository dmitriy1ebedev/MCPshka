package diagcontract

import (
	"strings"
	"testing"
)

func TestDiagBoundedExecutorPinsCommandAndLimits(t *testing.T) {
	args := []string{"native", "0.1.1", strings.Repeat("a", 64)}
	argv := BoundedExecArgs("diag-smoke", SmokeHelper, args)
	need := []string{
		"--property=RuntimeMaxSec=25s", "--property=MemoryMax=256M",
		"--property=CPUQuota=50%", "--property=TasksMax=16",
		"--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes",
		"--property=ProtectHome=read-only", SmokeHelper,
	}
	for _, value := range need {
		found := false
		for _, actual := range argv {
			if actual == value {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing bound/pinned argument %q: %q", value, argv)
		}
	}
	if len(argv) < len(args) || strings.Join(argv[len(argv)-len(args):], "|") != strings.Join(args, "|") {
		t.Fatalf("unexpected helper argument propagation: %q", argv)
	}
	a := BoundedExecArgs("diag-smoke", SmokeHelper, args)
	if strings.Join(argv, "|") != strings.Join(a, "|") {
		t.Fatal("identical scheduling identity not deterministic")
	}
}

func TestDiagSmokeContractFailClosed(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for _, mode := range []string{"native", "docker", "astra"} {
		if err := ValidateSmoke([]string{mode, "0.1.1", sha}); err != nil {
			t.Fatalf("%s rejected: %v", mode, err)
		}
	}
	bad := [][]string{
		{"shell", "0.1.1", sha}, {"docker", "../x", sha}, {"docker", "0.1.1", "checksum"},
		{"docker", "0.1.1", strings.ToUpper(sha)}, {"docker", "0.1.1", sha, "--privileged"},
		{"docker", "0.1.1", sha, "/var/run/docker.sock"}, nil,
	}
	for _, v := range bad {
		if ValidateSmoke(v) == nil {
			t.Fatalf("accepted unsafe smoke %q", v)
		}
	}
}
func TestDiagReleaseContractFailClosed(t *testing.T) {
	sha := strings.Repeat("f", 64)
	capsule := "/var/lib/chatops/projects/diag/releases/.staging-0.1.1-0edfb8051b32/release-capsule-0.1.1.tar.gz"
	binary := "/var/lib/chatops/projects/diag/releases/.staging-0.1.1-0edfb8051b32/diag-linux-arm64"
	for _, v := range [][]string{
		{"icloud-upload", "0.1.1", capsule, sha}, {"deploy", "0.1.1", binary, sha},
		{"status", strings.Repeat("c", 32)},
	} {
		if err := ValidateRelease(v); err != nil {
			t.Fatalf("valid request %q rejected %v", v, err)
		}
	}
	for _, v := range [][]string{
		{"upload", "0.1.1", capsule, sha},
		{"gdrive-upload", "0.1.1", capsule, sha},
		{"icloud-upload", "0.1.1", "/root/.config/icloud/session", sha},
		{"icloud-upload", "0.1.1", capsule + "/../release-capsule-0.1.1.tar.gz", sha},
		{"icloud-upload", "0.1.1", "/tmp/release-capsule-0.1.1.tar.gz", sha},
		{"icloud-upload", "0.1.1", binary, sha},
		{"deploy", "0.1.1", capsule, sha},
		{"deploy", "0.1.1", "/usr/local/bin/diag", sha},
		{"icloud-upload", "0.1.2", capsule, sha},
		{"icloud-upload", "0.1.1", strings.Replace(capsule, "0edfb8051b32", "../../../../../", 1), sha},
		{"icloud-upload", "0.1.1", capsule, strings.ToUpper(sha)},
		{"icloud-upload", "0.1.1", capsule, sha, "gdrive:/arbitrary"},
		{"status", "../foreign"}, {"status", strings.Repeat("g", 32)},
		{"restore", "0.1.1", binary, sha}, nil,
	} {
		if ValidateRelease(v) == nil {
			t.Fatalf("accepted unsafe release %q", v)
		}
	}
}
func TestDiagReleaseCannotSelectDestinationOrRuntime(t *testing.T) {
	sha := strings.Repeat("a", 64)
	capsule := "/var/lib/chatops/projects/diag/releases/.staging-0.1.1-0edfb8051b32/release-capsule-0.1.1.tar.gz"
	for _, v := range [][]string{
		{"icloud-upload", "0.1.1", capsule, sha, "--account=/root/.config/icloud/session"},
		{"icloud-upload", "0.1.1", capsule, sha, "--remote=untrusted:"},
		{"deploy", "0.1.1", capsule, sha, "--shell"},
	} {
		if ValidateRelease(v) == nil {
			t.Fatalf("runtime override accepted: %q", v)
		}
	}
}
