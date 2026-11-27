package gomodcache

import (
	"strings"
	"testing"
)

// The warm-up may reach only the checksum-verified proxy: no VCS, no
// direct fallback, no foreign toolchain, root cache as the only target.
func TestWarmArgsStayInsideTheTrustedPath(t *testing.T) {
	args := strings.Join(WarmArgs("mcpshka-gomod-warm-x", "/run/mcpshka-gomod-warm/x"), "\n")
	for _, want := range []string{"--setenv=GOPROXY=https://proxy.golang.org\n", "--setenv=GOSUMDB=sum.golang.org", "--setenv=GOVCS=*:off",
		"--setenv=GOTOOLCHAIN=local", "--setenv=GONOSUMDB=\n", "--setenv=GOMODCACHE=/root/.local/share/go/pkg/mod",
		"--property=ProtectSystem=strict", "--property=ReadWritePaths=/root/.local/share/go /run/mcpshka-gomod-warm/x",
		"--property=CapabilityBoundingSet=\n", "--property=RuntimeMaxSec=300s", "--\n/usr/bin/go\nmod\ndownload"} {
		if !strings.Contains(args+"\n", want) {
			t.Fatalf("missing %q in\n%s", want, args)
		}
	}
	if strings.Contains(args, "direct") {
		t.Fatal("direct VCS fallback allowed")
	}
}
