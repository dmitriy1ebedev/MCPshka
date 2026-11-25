package controlplane

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

const activeServices = "Id=mcpshka.service\nLoadState=loaded\nActiveState=active\nSubState=running\n\n" +
	"Id=mcpshka-tunnel.service\nLoadState=loaded\nActiveState=active\nSubState=running\n\n" +
	"Id=tailscaled.service\nLoadState=loaded\nActiveState=active\nSubState=running\n\n" +
	"Id=laned.service\nLoadState=loaded\nActiveState=active\nSubState=running\n"

func testControlService(t *testing.T) *Service {
	t.Helper()
	run := func(ctx context.Context, path string, args ...string) ([]byte, bool, error) {
		switch path {
		case "/fake/systemctl":
			return []byte(activeServices), false, nil
		case "/fake/tailscale":
			return []byte(`{"BackendState":"Running","TailscaleIPs":["100.77.0.10"],"Self":{"Online":true},"Peer":{"nodekey:test":{"HostName":"vps-canary","DNSName":"vps-canary.example.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.10","fd7a:115c:a1e0::10"],"Online":true,"Active":true,"ExitNode":true},"nodekey:infra":{"HostName":"funnel-ingress-node","TailscaleIPs":["fd7a:115c:a1e0::1"],"Online":true}}}`), false, nil
		default:
			return nil, false, errors.New("unexpected command")
		}
	}
	probe := func(ctx context.Context, url string) (bool, string) {
		switch url {
		case "http://127.0.0.1:8765/healthz":
			return true, "http=200"
		case "http://127.0.0.1:8768/readyz":
			return true, "http=200 body=ready"
		default:
			return false, "unexpected"
		}
	}
	s := newService(run, probe, func() ([]string, error) {
		return []string{"192.168.77.10"}, nil
	}, "/fake/systemctl", "/fake/tailscale")
	s.rollback = func() bool { return true }
	return s
}

func TestSnapshotIdentifiesPrimaryAndUnverifiedBackups(t *testing.T) {
	s := testControlService(t)
	out, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Primary != "openai_tunnel" || out.EmergencyState != "healthy" || !out.LANEAuthoritative || !out.RollbackReady {
		t.Fatalf("unexpected snapshot: %+v", out)
	}
	if len(out.VerifiedBackups) != 0 {
		t.Fatalf("unexpected verified backups: %+v", out.VerifiedBackups)
	}
	ts, ok := findPath(out, "tailscale")
	if !ok || !ts.TransportReady || ts.ManagementReady || ts.Verified {
		t.Fatalf("unexpected tailscale path: %+v", ts)
	}
	if len(out.TailscalePeers) != 1 {
		t.Fatalf("unexpected tailscale peers: %+v", out.TailscalePeers)
	}
	peer := out.TailscalePeers[0]
	if peer.HostName != "vps-canary" || !peer.Online || !peer.Active || !peer.ExitNode ||
		len(peer.TailscaleIPs) != 2 {
		t.Fatalf("unexpected tailscale peer: %+v", peer)
	}
	lan, ok := findPath(out, "lan")
	if !ok || !lan.TransportReady || lan.ManagementReady {
		t.Fatalf("unexpected LAN path: %+v", lan)
	}
}

func TestLANInventoryUnknownFailsClosedWithoutFalselyReportingDown(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reader     addressReader
		wantState  string
		wantReason string
	}{
		{name: "unavailable provider", reader: nil, wantState: "unknown", wantReason: "LAN address inventory unavailable; management path unverified"},
		{name: "netlink failed", reader: func() ([]string, error) { return nil, errors.New("address family not supported by protocol") }, wantState: "unknown", wantReason: "LAN address inventory unavailable; management path unverified"},
		{name: "confirmed no addresses", reader: func() ([]string, error) { return []string{}, nil }, wantState: "down", wantReason: "no non-loopback LAN addresses observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testControlService(t)
			s.addresses = tc.reader
			result, err := s.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			lan, ok := findPath(result, "lan")
			if !ok || lan.State != tc.wantState || lan.Reason != tc.wantReason ||
				lan.TransportReady || lan.ManagementReady || lan.Verified || len(result.VerifiedBackups) != 0 {
				t.Fatalf("unverified LAN observation promoted: %+v backups=%+v", lan, result.VerifiedBackups)
			}
			preflight, err := s.Preflight(context.Background(), MutationRequest{
				Kind: "tunnel_restart", Staged: true, RollbackSeconds: 60,
			})
			if err != nil {
				t.Fatal(err)
			}
			if preflight.Allowed || preflight.Decision != DecisionForbidden || preflight.BlastRadius.VerifiedBackupExists {
				t.Fatalf("unknown LAN incorrectly authorizes tunnel restart: %+v", preflight)
			}
		})
	}
}

func TestTailscaleProbeUnavailableCannotBecomeBackup(t *testing.T) {
	cases := []struct {
		name, payload, wantState string
		missingCLI, truncated    bool
		probeErr                 error
	}{
		{name: "no CLI", missingCLI: true, wantState: "unknown"},
		{name: "CLI error", probeErr: errors.New("unavailable"), wantState: "unknown"},
		{name: "truncated response", payload: "{}", truncated: true, wantState: "unknown"},
		{name: "malformed JSON", payload: "{", wantState: "unknown"},
		{name: "missing backend state", payload: "{}", wantState: "unknown"},
		{name: "confirmed offline", payload: `{"BackendState":"Stopped","Self":{"Online":false}}`, wantState: "down"},
		{name: "confirmed transport", payload: `{"BackendState":"Running","Self":{"Online":true}}`, wantState: "ready"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testControlService(t)
			if tc.missingCLI {
				s.tailscale = ""
			} else {
				s.run = func(_ context.Context, command string, _ ...string) ([]byte, bool, error) {
					if command == "/fake/systemctl" {
						return []byte(activeServices), false, nil
					}
					if command == "/fake/tailscale" {
						return []byte(tc.payload), tc.truncated, tc.probeErr
					}
					return nil, false, errors.New("unexpected command")
				}
			}
			snap, err := s.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			path, ok := findPath(snap, "tailscale")
			if !ok || path.State != tc.wantState || path.ManagementReady ||
				path.Verified || len(snap.VerifiedBackups) != 0 {
				t.Fatalf("unverified overlay promoted: %+v backups=%+v", path, snap.VerifiedBackups)
			}
			if path.TransportReady != (tc.wantState == "ready") {
				t.Fatalf("incorrect transport readiness: %+v", path)
			}
			if tc.wantState == "unknown" && path.Reason == "" {
				t.Fatal("unknown overlay missing reason")
			}
			rule, err := s.Preflight(context.Background(), MutationRequest{
				Kind: "tunnel_restart", Staged: true, RollbackSeconds: 60,
			})
			if err != nil || rule.Allowed || rule.Decision != DecisionForbidden {
				t.Fatalf("unsafe tunnel mutation admitted: %+v %v", rule, err)
			}
		})
	}
}

func TestPreflightProtectsOnlyVerifiedExternalPath(t *testing.T) {
	s := testControlService(t)

	self, err := s.Preflight(context.Background(), MutationRequest{Kind: "self_deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if !self.Allowed || self.Decision != DecisionGuarded {
		t.Fatalf("self deploy: %+v", self)
	}

	directNet, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "dns", ViaProvider: "direct", AffectedPaths: []string{"openai_tunnel"},
		Staged: true, RollbackSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if directNet.Decision != DecisionForbidden || directNet.Allowed {
		t.Fatalf("direct network mutation: %+v", directNet)
	}

	laneNet, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "dns", ViaProvider: "lane", AffectedPaths: []string{"openai_tunnel"},
		Staged: true, RollbackSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if laneNet.Decision != DecisionForbidden || !laneNet.BlastRadius.ControlLossPossible {
		t.Fatalf("unsafe lane mutation: %+v", laneNet)
	}

	tunnel, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "tunnel_restart", Staged: true, RollbackSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tunnel.Decision != DecisionForbidden {
		t.Fatalf("tunnel restart without backup: %+v", tunnel)
	}
}

func TestPreflightRequiresProviderPlanAndVerifiedCutover(t *testing.T) {
	s := testControlService(t)
	unknown, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "vpn", ViaProvider: "lane", Staged: true, RollbackSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Decision != DecisionApprovalRequired ||
		!slices.Contains(unknown.Requirements, "obtain provider plan with affected management paths") {
		t.Fatalf("unknown blast radius: %+v", unknown)
	}

	cutover, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "vpn", ViaProvider: "lane", AffectedPaths: []string{"lan"},
		Staged: true, RollbackSeconds: 60, DisableOldPath: true, NewPath: "tailscale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cutover.Decision != DecisionForbidden {
		t.Fatalf("unverified cutover accepted: %+v", cutover)
	}
}

func TestNetworkMutationRejectsUnknownAndEmptyAffectedPaths(t *testing.T) {
	for _, tc := range []struct {
		name       string
		paths      []string
		wantReason string
	}{
		{name: "empty strings do not establish blast radius", paths: []string{" ", ""}, wantReason: "network blast radius is unknown"},
		{name: "unknown claimed path cannot hide affected primary", paths: []string{"imaginary_backup"}, wantReason: "network blast radius contains an unknown control path"},
		{name: "known and unknown mix is still unknown", paths: []string{"lan", "imaginary_backup"}, wantReason: "network blast radius contains an unknown control path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := testControlService(t).Preflight(context.Background(), MutationRequest{
				Kind: "vpn", ViaProvider: "lane", AffectedPaths: tc.paths,
				Staged: true, RollbackSeconds: 60,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Decision != DecisionApprovalRequired || result.Allowed || result.Reason != tc.wantReason {
				t.Fatalf("unverified blast radius accepted: %+v", result)
			}
		})
	}
}

func TestCutoverCannotVerifyAPathItAlsoMutates(t *testing.T) {
	s := testControlService(t)
	unsafe, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "dns", ViaProvider: "lane", AffectedPaths: []string{"openai_tunnel"},
		DisableOldPath: true, NewPath: "openai_tunnel", Staged: true, RollbackSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.Decision != DecisionForbidden || unsafe.Allowed ||
		unsafe.Reason != "new management path is inside the affected blast radius" {
		t.Fatalf("self-affected cutover accepted: %+v", unsafe)
	}
	upstream, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "dns", ViaProvider: "lane", AffectedPaths: []string{"local_loopback"},
		DisableOldPath: true, NewPath: "openai_tunnel", Staged: true, RollbackSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Decision != DecisionForbidden || upstream.Allowed ||
		upstream.Reason != "new management path is inside the affected blast radius" ||
		!upstream.BlastRadius.PrimaryAffected || !upstream.BlastRadius.ControlLossPossible {
		t.Fatalf("MCP tunnel reused while its local upstream is affected: %+v", upstream)
	}
	// A provider-declared disjoint plan passes this preflight, but remains
	// subject to LANE staged verification and rollback; this is not proof
	// of the physical underlay or authorization to change network state.
	safe, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "dns", ViaProvider: "lane", AffectedPaths: []string{"lan"},
		DisableOldPath: true, NewPath: "openai_tunnel", Staged: true, RollbackSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if safe.Decision != DecisionGuarded || !safe.Allowed || safe.BlastRadius.PrimaryAffected {
		t.Fatalf("safe disjoint staged cutover refused: %+v", safe)
	}
}

func TestBlastRadiusExcludesPrimaryAndAffectedMCPBackups(t *testing.T) {
	snapshot := Snapshot{
		Primary: "openai_tunnel",
		Paths: []Path{
			{ID: "local_loopback", Kind: "local", ManagementReady: true, Verified: true},
			{ID: "openai_tunnel", Kind: "outbound_mcp_tunnel", External: true, ManagementReady: true, Verified: true},
			{ID: "second_mcp", Kind: "outbound_mcp_tunnel", External: true, ManagementReady: true, Verified: true},
		},
	}
	blast := blastRadius(snapshot, []string{"local_loopback"})
	if !blast.PrimaryAffected || !blast.ControlLossPossible || blast.VerifiedBackupExists {
		t.Fatalf("affected local MCP service falsely keeps tunnel backup: %+v", blast)
	}
	// A separately verified, independent OOB channel can remain a backup.
	snapshot.Paths = append(snapshot.Paths, Path{
		ID: "independent_oob", Kind: "oob_management", External: true,
		ManagementReady: true, Verified: true,
	})
	blast = blastRadius(snapshot, []string{"local_loopback"})
	if !blast.PrimaryAffected || blast.ControlLossPossible || !blast.VerifiedBackupExists {
		t.Fatalf("independent OOB backup not distinguished: %+v", blast)
	}
	blast = blastRadius(snapshot, []string{"local_loopback", "independent_oob"})
	if !blast.PrimaryAffected || !blast.ControlLossPossible || blast.VerifiedBackupExists {
		t.Fatalf("mutated OOB path counted as backup: %+v", blast)
	}
}

func TestDegradedStateAllowsRecoveryOnly(t *testing.T) {
	s := testControlService(t)
	s.probe = func(ctx context.Context, url string) (bool, string) {
		if url == "http://127.0.0.1:8765/healthz" {
			return true, "http=200"
		}
		return false, "down"
	}
	blocked, err := s.Preflight(context.Background(), MutationRequest{Kind: "self_deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Decision != DecisionForbidden {
		t.Fatalf("mutation allowed while degraded: %+v", blocked)
	}
	recovery, err := s.Preflight(context.Background(), MutationRequest{Kind: "recovery"})
	if err != nil {
		t.Fatal(err)
	}
	if !recovery.Allowed || recovery.Decision != DecisionGuarded {
		t.Fatalf("recovery blocked: %+v", recovery)
	}
}

func TestDecisionEscalationIsMonotonic(t *testing.T) {
	classes := []string{DecisionSafe, DecisionGuarded, DecisionApprovalRequired, DecisionForbidden}
	for i, current := range classes {
		for j, next := range classes {
			want := current
			if j > i {
				want = next
			}
			if got := EscalateDecision(current, next); got != want {
				t.Fatalf("EscalateDecision(%q,%q)=%q want=%q", current, next, got, want)
			}
		}
	}
	if got := EscalateDecision("UNKNOWN", DecisionSafe); got != DecisionForbidden {
		t.Fatalf("unknown class did not fail closed: %q", got)
	}
}

func TestPreflightDecisionTable(t *testing.T) {
	tests := []struct {
		name       string
		req        MutationRequest
		want       string
		allowed    bool
		reversible bool
		recovery   bool
	}{
		{name: "read only", req: MutationRequest{Kind: "read_only"}, want: DecisionSafe, allowed: true, reversible: true, recovery: true},
		{name: "self deploy", req: MutationRequest{Kind: "self_deploy"}, want: DecisionGuarded, allowed: true, reversible: true, recovery: true},
		{name: "lane staged dns", req: MutationRequest{Kind: "dns", ViaProvider: "lane", AffectedPaths: []string{"lan"}, Staged: true, RollbackSeconds: 60}, want: DecisionGuarded, allowed: true, reversible: true, recovery: true},
		{name: "unknown blast radius", req: MutationRequest{Kind: "vpn", ViaProvider: "lane", Staged: true, RollbackSeconds: 60}, want: DecisionApprovalRequired, allowed: false, reversible: true, recovery: true},
		{name: "unknown mutation", req: MutationRequest{Kind: "project_specific"}, want: DecisionApprovalRequired, allowed: false},
		{name: "direct lane bypass", req: MutationRequest{Kind: "dns", ViaProvider: "direct", AffectedPaths: []string{"lan"}, Staged: true, RollbackSeconds: 60}, want: DecisionForbidden, allowed: false, reversible: true, recovery: true},
		{name: "only tunnel", req: MutationRequest{Kind: "tunnel_restart", Staged: true, RollbackSeconds: 60}, want: DecisionForbidden, allowed: false, reversible: true, recovery: false},
		{name: "agent restart", req: MutationRequest{Kind: "agent_restart"}, want: DecisionForbidden, allowed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := testControlService(t).Preflight(context.Background(), tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if out.Decision != tt.want || out.Allowed != tt.allowed {
				t.Fatalf("decision=%s allowed=%v want=%s/%v: %+v", out.Decision, out.Allowed, tt.want, tt.allowed, out)
			}
			if out.Recovery.Reversible != tt.reversible {
				t.Fatalf("reversible=%v want=%v: %+v", out.Recovery.Reversible, tt.reversible, out.Recovery)
			}
			if out.Recovery.RecoveryPathAvailable != tt.recovery {
				t.Fatalf("recovery_path_available=%v want=%v: %+v", out.Recovery.RecoveryPathAvailable, tt.recovery, out.Recovery)
			}
			if out.Recovery.CurrentHealth != "healthy" {
				t.Fatalf("health evidence=%q", out.Recovery.CurrentHealth)
			}
		})
	}
}

func TestPreflightNoticesAreStable(t *testing.T) {
	s := testControlService(t)
	first, err := s.Preflight(context.Background(), MutationRequest{Kind: "project_specific"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Preflight(context.Background(), MutationRequest{Kind: "project_specific"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Notice == nil || first.Notice.Type != "approval_request" || first.Notice.Severity != "warning" || first.Notice.DedupKey == "" {
		t.Fatalf("unexpected approval notice: %+v", first.Notice)
	}
	if second.Notice == nil || second.Notice.DedupKey != first.Notice.DedupKey {
		t.Fatalf("approval notice dedup key changed: first=%+v second=%+v", first.Notice, second.Notice)
	}

	recovery, err := s.Preflight(context.Background(), MutationRequest{Kind: "recovery"})
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Notice == nil || recovery.Notice.Type != "recovery_notice" || recovery.Notice.Severity != "info" || recovery.Notice.DedupKey == "" {
		t.Fatalf("unexpected recovery notice: %+v", recovery.Notice)
	}
}

func TestParseServiceStates(t *testing.T) {
	got := parseServiceStates([]byte(activeServices))
	if !isActive(got["mcpshka.service"]) || !isActive(got["laned.service"]) {
		t.Fatalf("unexpected states: %#v", got)
	}
}

func TestDegradedStateKeepsReadOnlyInspectionAvailable(t *testing.T) {
	for _, kind := range []string{"read_only", "inspect", "status", "probe"} {
		t.Run(kind, func(t *testing.T) {
			s := testControlService(t)
			s.probe = func(context.Context, string) (bool, string) { return false, "down" }
			out, err := s.Preflight(context.Background(), MutationRequest{Kind: kind})
			if err != nil {
				t.Fatal(err)
			}
			if !out.Allowed || out.Decision != DecisionSafe {
				t.Fatalf("read-only recovery inspection blocked: %+v", out)
			}
			if out.Snapshot.EmergencyState == "healthy" {
				t.Fatal("test did not exercise degraded control path")
			}
		})
	}
}

// MCP-103: a next-boot Lane change has no live blast radius; without a
// verified reserve the answer is one clear step, not "obtain provider plan".
func TestNextBootLaneChangeNeedsOnlyVerifiedReserve(t *testing.T) {
	s := testControlService(t)
	got, err := s.Preflight(context.Background(), MutationRequest{
		Kind: "network", ViaProvider: "lane", Effective: "next_boot", Staged: true, RollbackSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.BlastRadius.VerifiedBackupExists {
		t.Fatal("fixture unexpectedly has a verified reserve")
	}
	if got.Allowed || got.Decision != DecisionApprovalRequired || len(got.Requirements) != 1 ||
		!strings.Contains(got.Requirements[0], "verify an independent management path") {
		t.Fatalf("next-boot without reserve: %+v", got)
	}
	live, _ := s.Preflight(context.Background(), MutationRequest{Kind: "network", ViaProvider: "lane", Staged: true, RollbackSeconds: 600})
	if live.Reason != "network blast radius is unknown" {
		t.Fatalf("live change with empty paths must stay unknown: %+v", live)
	}
}
