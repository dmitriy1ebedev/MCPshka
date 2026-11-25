package installplan

import "testing"

// MCP-079: blockers before any change; re-running on an installed host skips
// what is already there; every step names blast radius and rollback.
func TestInstallPlanPreflightAndIdempotence(t *testing.T) {
	bad := Build(Facts{Arch: "386", FreeBytes: 1 << 20, PortsBusy: map[int]bool{Ports[0]: true}})
	if bad.Ready || len(bad.Blockers) != 4 {
		t.Fatalf("blockers: %+v", bad.Blockers)
	}
	ok := Build(Facts{Arch: "arm64", Systemd: true, FreeBytes: 8 << 30, PortsBusy: map[int]bool{Ports[0]: true}, Installed: "0.2.9", ServiceUser: true})
	if !ok.Ready {
		t.Fatalf("installed host blocked: %+v", ok.Blockers)
	}
	skipped := 0
	for _, s := range ok.Steps {
		if s.Blast == "" || s.Rollback == "" {
			t.Fatalf("step without blast/rollback: %+v", s)
		}
		if s.Skip {
			skipped++
		}
	}
	if skipped != 2 {
		t.Fatalf("idempotent re-run skipped %d steps", skipped)
	}
}
