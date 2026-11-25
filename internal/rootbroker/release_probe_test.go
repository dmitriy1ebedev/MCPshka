package rootbroker

import "testing"

func TestReleaseProbeAllowsOnlyFixedImagesAndReadOperations(t *testing.T) {
	for _, path := range []string{"/usr/local/bin/mcpshka", "/usr/local/bin/mcpshka.previous", "/root/repos/mcpshka/bin/mcpshka-0.1.99-r1.candidate", "/usr/local/bin/.mcpshka.restore.abc123"} {
		for _, op := range []string{"version", "state-store-support"} {
			if err := validateReleaseProbe([]string{path, op}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, args := range [][]string{{"/etc/mcpshka/auth.json", "version"}, {"/usr/local/bin/sh", "version"}, {"/root/repos/mcpshka/bin/../secret", "version"}, {"/usr/local/bin/mcpshka", "shell"}, {"/usr/local/bin/mcpshka", "version", "--unsafe"}} {
		if validateReleaseProbe(args) == nil {
			t.Fatalf("unsafe release probe: %v", args)
		}
	}
}
