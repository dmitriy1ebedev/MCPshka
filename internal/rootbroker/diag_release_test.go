package rootbroker

import (
	"mcpshka/internal/diagprovider"
	"strings"
	"testing"
)

func TestValidateDiagInstall(t *testing.T) {
	ok := []string{"diag-release", strings.Repeat("a", 32), "owner", strings.Repeat("b", 40), "0.1.11"}
	if err := validateDiagInstall(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{
		{"diag-release", strings.Repeat("a", 32), "owner", strings.Repeat("b", 39), "0.1.11"},
		{"diag-release", strings.Repeat("a", 32), "owner", strings.Repeat("b", 40), "0.1.11;rm"},
		{"diag-release", strings.Repeat("a", 32), "local-stdio", strings.Repeat("b", 40), "0.1.11"},
	} {
		if validateDiagInstall(bad) == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if diagprovider.ReleaseUnit("0.1.11-dev.1") != "mcpshka-diag-release-0111dev1" {
		t.Fatal(diagprovider.ReleaseUnit("0.1.11-dev.1"))
	}
}

func TestHealthUnitsReadableNotMutable(t *testing.T) {
	cfg := Config{}
	if validateSystemctl(cfg, []string{"show", "--no-pager", "--property=ActiveState", "docker.service"}) != nil {
		t.Fatal("health unit status refused")
	}
	if validateSystemctl(cfg, []string{"restart", "docker.service"}) == nil {
		t.Fatal("health unit became restartable")
	}
	if validateSystemctl(cfg, []string{"show", "--no-pager", "--property=ActiveState", "ssh.service"}) == nil {
		t.Fatal("unlisted unit readable")
	}
}

func TestReverseDependencyPropertiesReadable(t *testing.T) {
	cfg := Config{}
	if e := validateSystemctl(cfg, []string{"show", "--no-pager", "--property=Id,RequiredBy,BoundBy,ConsistsOf", "docker.service"}); e != nil {
		t.Fatalf("cascade check refused (MCP-160): %v", e)
	}
	e := validateSystemctl(cfg, []string{"show", "--no-pager", "--property=Id,Environment", "docker.service"})
	if e == nil || !strings.Contains(e.Error(), `"Environment"`) {
		t.Fatalf("error must name the property: %v", e)
	}
}

// 14.11.2026: a DIAG build beside agent tests drove H96 to load 14 and the
// home internet stalled; the build must yield CPU and wait for the queue.
func TestDiagReleaseYieldsToNetworkAndQueues(t *testing.T) {
	args := strings.Join(diagprovider.ReleaseUnitArgs("mcpshka-diag-release-0-6-2", strings.Repeat("a", 40), "0.6.2"), " ")
	for _, want := range []string{"--property=Slice=mcpshka-heavy.slice", "--property=CPUWeight=20", "--property=IOWeight=10", "--property=Nice=10", "--property=IOSchedulingClass=idle"} {
		if !strings.Contains(args, want) {
			t.Fatal("diag release unit lacks", want, args)
		}
	}
}
