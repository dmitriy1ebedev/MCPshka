package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

const (
	DecisionSafe             = "SAFE"
	DecisionGuarded          = "GUARDED"
	DecisionApprovalRequired = "APPROVAL_REQUIRED"
	DecisionForbidden        = "FORBIDDEN"

	commandTimeout = 12 * time.Second
	maxOutputBytes = 64 * 1024
)

var decisionRank = map[string]int{
	DecisionSafe:             0,
	DecisionGuarded:          1,
	DecisionApprovalRequired: 2,
	DecisionForbidden:        3,
}

// BaseDecision returns the minimum static risk class for an operation kind.
// Runtime evidence may only escalate this class through EscalateDecision.
func BaseDecision(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "read_only", "inspect", "status", "probe":
		return DecisionSafe
	case "self_deploy", "recovery", "tunnel_restart", "network", "vpn", "dns", "routes", "firewall":
		return DecisionGuarded
	case "agent_restart":
		return DecisionForbidden
	default:
		return DecisionApprovalRequired
	}
}

// EscalateDecision enforces the monotonic-risk law: automatic evaluation may
// only retain or increase a risk class. Unknown classes fail closed.
func EscalateDecision(current, next string) string {
	currentRank, currentOK := decisionRank[current]
	nextRank, nextOK := decisionRank[next]
	if !currentOK || !nextOK {
		return DecisionForbidden
	}
	if nextRank > currentRank {
		return next
	}
	return current
}

func decisionAllowsExecution(decision string) bool {
	return decision == DecisionSafe || decision == DecisionGuarded
}

type Path struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Address         string `json:"address,omitempty"`
	State           string `json:"state"`
	TransportReady  bool   `json:"transport_ready"`
	ManagementReady bool   `json:"management_ready"`
	Verified        bool   `json:"verified"`
	External        bool   `json:"external"`
	Reason          string `json:"reason,omitempty"`
}

type TailscalePeer struct {
	HostName     string   `json:"host_name,omitempty"`
	DNSName      string   `json:"dns_name,omitempty"`
	OS           string   `json:"os,omitempty"`
	TailscaleIPs []string `json:"tailscale_ips,omitempty"`
	Online       bool     `json:"online"`
	Active       bool     `json:"active"`
	ExitNode     bool     `json:"exit_node"`
}

type Snapshot struct {
	CapturedAt        time.Time       `json:"captured_at"`
	Primary           string          `json:"primary,omitempty"`
	Paths             []Path          `json:"paths"`
	VerifiedBackups   []string        `json:"verified_backups"`
	TailscalePeers    []TailscalePeer `json:"tailscale_peers,omitempty"`
	LANEAuthoritative bool            `json:"lane_authoritative"`
	RollbackReady     bool            `json:"rollback_ready"`
	EmergencyState    string          `json:"emergency_state"`
	Loss              Loss            `json:"loss_detection"`
	Warnings          []string        `json:"warnings,omitempty"`
	ControlRoutes     []OutboundRoute `json:"control_routes,omitempty"`
}

type MutationRequest struct {
	Kind            string   `json:"kind"`
	ViaProvider     string   `json:"via_provider,omitempty"`
	AffectedPaths   []string `json:"affected_paths,omitempty"`
	Staged          bool     `json:"staged,omitempty"`
	RollbackSeconds int      `json:"rollback_seconds,omitempty"`
	NewPath         string   `json:"new_path,omitempty"`
	DisableOldPath  bool     `json:"disable_old_path,omitempty"`
	Effective       string   `json:"effective,omitempty"` // live (default) | next_boot
}

type BlastRadius struct {
	AffectedPaths        []string `json:"affected_paths"`
	PrimaryAffected      bool     `json:"primary_affected"`
	VerifiedBackupExists bool     `json:"verified_backup_exists"`
	ControlLossPossible  bool     `json:"control_loss_possible"`
}

type RecoveryEvidence struct {
	CurrentHealth         string `json:"current_health"`
	Reversible            bool   `json:"reversible"`
	RecoveryPathAvailable bool   `json:"recovery_path_available"`
	RollbackReady         bool   `json:"rollback_ready"`
	Staged                bool   `json:"staged"`
	RollbackSeconds       int    `json:"rollback_seconds,omitempty"`
}

type Notice struct {
	Type     string `json:"type"`
	Severity string `json:"severity"`
	DedupKey string `json:"dedup_key"`
	Message  string `json:"message"`
}

type Preflight struct {
	Operation    string           `json:"operation"`
	Decision     string           `json:"decision"`
	Allowed      bool             `json:"allowed"`
	Reason       string           `json:"reason"`
	BlastRadius  BlastRadius      `json:"blast_radius"`
	Recovery     RecoveryEvidence `json:"recovery"`
	Notice       *Notice          `json:"notice,omitempty"`
	Requirements []string         `json:"requirements,omitempty"`
	Snapshot     Snapshot         `json:"snapshot"`
}

type commandRunner func(context.Context, string, ...string) ([]byte, bool, error)
type probe func(context.Context, string) (bool, string)
type addressReader func() ([]string, error)

type Service struct {
	run       commandRunner
	probe     probe
	addresses addressReader
	rollback  func() bool
	readFile  func(string) ([]byte, error)
	resolveIP func(context.Context, string) ([]net.IPAddr, error)
	systemctl string
	tailscale string
	ip        string
	loss      lossDetector
}

func New() *Service {
	return &Service{
		run:       runBounded,
		probe:     httpProbe,
		addresses: localAddresses,
		rollback:  func() bool { return executableRegular("/usr/local/bin/mcpshka.previous") },
		systemctl: firstExisting("/usr/bin/systemctl", "/bin/systemctl"),
		tailscale: firstExisting("/usr/bin/tailscale", "/usr/local/bin/tailscale"),
		readFile:  readProcFile,
		resolveIP: net.DefaultResolver.LookupIPAddr,
		ip:        firstExisting("/usr/sbin/ip", "/sbin/ip", "/usr/bin/ip", "/bin/ip"),
	}
}

func newService(run commandRunner, probe probe, addresses addressReader, systemctl, tailscale string) *Service {
	return &Service{
		run: run, probe: probe, addresses: addresses,
		rollback:  func() bool { return false },
		systemctl: systemctl, tailscale: tailscale,
	}
}

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	out := Snapshot{CapturedAt: time.Now().UTC(), Paths: []Path{}, VerifiedBackups: []string{}}
	if s.systemctl == "" {
		return Snapshot{}, errors.New("systemctl not found")
	}

	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	raw, truncated, err := s.run(child, s.systemctl, "show",
		"mcpshka.service", "mcpshka-tunnel.service", "tailscaled.service", "laned.service",
		"--no-pager", "--property=Id,LoadState,ActiveState,SubState")
	if err != nil {
		return Snapshot{}, fmt.Errorf("systemctl show: %w: %s", err, bounded(raw))
	}
	if truncated {
		return Snapshot{}, errors.New("systemctl control-path output exceeded limit")
	}
	states := parseServiceStates(raw)
	agentActive := isActive(states["mcpshka.service"])
	tunnelActive := isActive(states["mcpshka-tunnel.service"])
	tailscaleActive := isActive(states["tailscaled.service"])
	out.LANEAuthoritative = isActive(states["laned.service"])
	if s.rollback != nil {
		out.RollbackReady = s.rollback()
	}

	healthOK, healthDetail := s.probe(child, "http://127.0.0.1:8765/healthz")
	readyOK, readyDetail := s.probe(child, "http://127.0.0.1:8768/readyz")
	local := Path{
		ID: "local_loopback", Kind: "local", Address: "127.0.0.1:8765", External: false,
		State: stateWord(agentActive && healthOK), TransportReady: agentActive, ManagementReady: agentActive && healthOK,
		Verified: agentActive && healthOK,
	}
	if !local.Verified {
		local.Reason = compactReason(healthDetail)
	}
	out.Paths = append(out.Paths, local)

	tunnel := Path{
		ID: "openai_tunnel", Kind: "outbound_mcp_tunnel", External: true,
		State:           stateWord(agentActive && tunnelActive && readyOK),
		TransportReady:  tunnelActive && readyOK,
		ManagementReady: agentActive && tunnelActive && readyOK,
		Verified:        agentActive && tunnelActive && readyOK,
	}
	if !tunnel.Verified {
		tunnel.Reason = compactReason(readyDetail)
	}
	out.Paths = append(out.Paths, tunnel)
	if tunnel.Verified {
		out.Primary = tunnel.ID
	}

	var selfIPs []net.IP
	onlinePeers := map[string]bool{}
	tsPath := Path{ID: "tailscale", Kind: "overlay_transport", External: true,
		State: "down", Reason: "tailscaled service inactive"}
	if tailscaleActive {
		// Service liveness alone does not prove overlay transport is reachable.
		// A missing CLI, failed probe, or malformed result is UNKNOWN, not DOWN.
		tsPath.State = "unknown"
		tsPath.Reason = "tailscale transport status unavailable; MCP backup unverified"
		if s.tailscale == "" {
			out.Warnings = append(out.Warnings, "tailscale status CLI unavailable")
		} else {
			tsRaw, tsTruncated, tsErr := s.run(child, s.tailscale, "status", "--json")
			if tsErr != nil {
				out.Warnings = append(out.Warnings, "tailscale status: "+tsErr.Error())
			} else if tsTruncated {
				out.Warnings = append(out.Warnings, "tailscale status truncated")
			} else {
				var status struct {
					BackendState string
					TailscaleIPs []string
					Self         struct {
						Online bool
					}
					Peer map[string]struct {
						HostName     string
						DNSName      string
						OS           string
						TailscaleIPs []string
						Online       bool
						Active       bool
						ExitNode     bool
					}
				}
				if err := json.Unmarshal(tsRaw, &status); err != nil {
					out.Warnings = append(out.Warnings, "tailscale decode: "+err.Error())
				} else if status.BackendState == "" {
					out.Warnings = append(out.Warnings, "tailscale status missing backend state")
				} else {
					tsPath.TransportReady = status.BackendState == "Running" && status.Self.Online
					tsPath.State = stateWord(tsPath.TransportReady)
					tsPath.Reason = "tailscale transport not ready; MCP backup unverified"
					if len(status.TailscaleIPs) > 0 {
						tsPath.Address = status.TailscaleIPs[0]
					}
					for _, raw := range status.TailscaleIPs {
						if ip := net.ParseIP(raw); ip != nil {
							selfIPs = append(selfIPs, ip)
						}
					}
					if tsPath.TransportReady {
						tsPath.Reason = "transport online; direct MCP management endpoint is not configured"
					}
					for _, peer := range status.Peer {
						item := TailscalePeer{
							HostName: peer.HostName,
							DNSName:  peer.DNSName,
							OS:       peer.OS,
							Online:   peer.Online,
							Active:   peer.Active,
							ExitNode: peer.ExitNode,
						}
						for _, rawIP := range peer.TailscaleIPs {
							if len(item.TailscaleIPs) >= 4 {
								break
							}
							if net.ParseIP(rawIP) != nil {
								item.TailscaleIPs = append(item.TailscaleIPs, rawIP)
							}
						}
						if item.DNSName == "" && item.OS == "" {
							continue
						}
						if peer.Online {
							for _, raw := range peer.TailscaleIPs {
								if ip := net.ParseIP(raw); ip != nil {
									onlinePeers[ip.String()] = true
								}
							}
						}
						out.TailscalePeers = append(out.TailscalePeers, item)
						if len(out.TailscalePeers) >= 64 {
							break
						}
					}
					sort.Slice(out.TailscalePeers, func(i, j int) bool {
						if out.TailscalePeers[i].HostName != out.TailscalePeers[j].HostName {
							return out.TailscalePeers[i].HostName < out.TailscalePeers[j].HostName
						}
						if out.TailscalePeers[i].DNSName != out.TailscalePeers[j].DNSName {
							return out.TailscalePeers[i].DNSName < out.TailscalePeers[j].DNSName
						}
						return strings.Join(out.TailscalePeers[i].TailscaleIPs, ",") < strings.Join(out.TailscalePeers[j].TailscaleIPs, ",")
					})
				}
			}
		}
	}
	out.Paths = append(out.Paths, tsPath)
	out.Paths = append(out.Paths, s.tailscaleSSHPath(child, tsPath.TransportReady, selfIPs, onlinePeers))

	// A failed address inventory is UNKNOWN, not proof that LAN is down.
	// Neither a LAN address nor Tailscale transport proves an MCP backup path.
	lan := Path{ID: "lan", Kind: "lan_transport", External: true, State: "unknown",
		Reason: "LAN address inventory unavailable; management path unverified"}
	if s.addresses != nil {
		addrs, err := s.addresses()
		if err != nil {
			out.Warnings = append(out.Warnings, "local addresses: "+err.Error())
		} else if len(addrs) == 0 {
			lan.State = "down"
			lan.Reason = "no non-loopback LAN addresses observed"
		} else {
			sort.Strings(addrs)
			lan.Address = addrs[0]
			lan.TransportReady = true
			lan.State = "ready"
			lan.Reason = "LAN address present; direct MCP management endpoint is loopback-only"
		}
	}
	out.Paths = append(out.Paths, lan)

	for _, p := range out.Paths {
		if p.External && p.ManagementReady && p.Verified && p.ID != out.Primary {
			out.VerifiedBackups = append(out.VerifiedBackups, p.ID)
		}
	}
	if out.Primary != "" {
		out.EmergencyState = "healthy"
	} else if tsPath.TransportReady || lan.TransportReady {
		out.EmergencyState = "degraded"
	} else {
		out.EmergencyState = "emergency"
	}
	out.ControlRoutes = []OutboundRoute{s.inspectControlRoute(ctx, "api.openai.com")}
	out.Loss = s.loss.observe(out.Primary != "", out.CapturedAt)
	return out, nil
}

func (s *Service) Preflight(ctx context.Context, req MutationRequest) (Preflight, error) {
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	if req.Kind == "" {
		return Preflight{}, errors.New("mutation kind is required")
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return Preflight{}, err
	}
	affected := dedupe(req.AffectedPaths)
	if len(affected) == 0 {
		affected = inferredAffected(req.Kind)
	}
	blast := blastRadius(snap, affected)
	baseDecision := BaseDecision(req.Kind)
	out := Preflight{
		Operation:   req.Kind,
		Decision:    baseDecision,
		Allowed:     decisionAllowsExecution(baseDecision),
		Snapshot:    snap,
		BlastRadius: blast,
		Recovery:    recoveryEvidence(snap, req, blast),
	}

	// Inspection must remain available during recovery; this branch cannot authorize mutations.
	if baseDecision == DecisionSafe {
		return decide(out, DecisionSafe, true, "read-only operation has no mutation blast radius"), nil
	}

	if snap.EmergencyState != "healthy" && req.Kind != "recovery" {
		return decide(out, DecisionForbidden, false, "control plane is already degraded; recover before unrelated mutation",
			"restore a verified management path"), nil
	}

	if req.DisableOldPath {
		newPath, ok := findPath(snap, req.NewPath)
		if !ok || !newPath.External || !newPath.ManagementReady || !newPath.Verified {
			return decide(out, DecisionForbidden, false, "new management path is not independently verified",
				"verify the new management path before disabling the old path"), nil
		}
		// A verified endpoint is not a safe cutover target when the same
		// staged mutation also changes or removes that endpoint.
		affectedSet := map[string]struct{}{}
		for _, id := range affected {
			affectedSet[id] = struct{}{}
		}
		if managementPathAffected(newPath, affectedSet) {
			return decide(out, DecisionForbidden, false, "new management path is inside the affected blast radius",
				"keep the verified new management path outside the provider change plan"), nil
		}
	}

	switch req.Kind {
	case "read_only", "inspect", "status", "probe":
		return decide(out, DecisionSafe, true, "read-only operation has no mutation blast radius"), nil

	case "self_deploy":
		if !snap.RollbackReady || snap.Primary == "" {
			return decide(out, DecisionForbidden, false, "guarded self-deploy requires a healthy primary path and validated rollback binary",
				"restore primary control path", "restore rollback binary"), nil
		}
		return decide(out, DecisionGuarded, true, "self-deploy has detached execution, runtime verification and rollback",
			"preserve tunnel service", "verify production after reconnect"), nil

	case "agent_restart":
		return decide(out, DecisionForbidden, false, "direct agent restart can sever the active MCP request",
			"use guarded self_deploy instead"), nil

	case "tunnel_restart":
		if !blast.VerifiedBackupExists {
			return decide(out, DecisionForbidden, false, "OpenAI tunnel is the only verified external MCP path",
				"establish and verify an independent management path first"), nil
		}
		if !req.Staged || req.RollbackSeconds < 30 || req.RollbackSeconds > 900 {
			return decide(out, DecisionApprovalRequired, false, "control-path restart requires staged execution and bounded timed rollback",
				"set staged=true", "set rollback_seconds between 30 and 900"), nil
		}
		return decide(out, DecisionGuarded, true, "independent verified backup path exists and timed rollback is armed"), nil

	case "network", "vpn", "dns", "routes", "firewall":
		if snap.LANEAuthoritative && strings.ToLower(strings.TrimSpace(req.ViaProvider)) != "lane" {
			return decide(out, DecisionForbidden, false, "LANE is authoritative for persistent network state on this node",
				"obtain lane.plan and apply through lane/laned"), nil
		}
		if len(affected) == 0 && req.Effective == "next_boot" && strings.EqualFold(strings.TrimSpace(req.ViaProvider), "lane") {
			return nextBoot(out, req, blast), nil
		}
		if len(affected) == 0 {
			return decide(out, DecisionApprovalRequired, false, "network blast radius is unknown",
				"obtain provider plan with affected management paths"), nil
		}
		// A supplied but empty or unrecognized path name cannot attest the
		// blast radius. Accept only identities from the observed control paths.
		for _, id := range affected {
			if _, ok := findPath(snap, id); !ok {
				return decide(out, DecisionApprovalRequired, false, "network blast radius contains an unknown control path",
					"obtain provider plan using known control_path_status identities"), nil
			}
		}
		if blast.PrimaryAffected && !blast.VerifiedBackupExists {
			return decide(out, DecisionForbidden, false, "mutation can affect the only verified external management path",
				"preserve primary path or verify an independent backup path"), nil
		}
		if !req.Staged || req.RollbackSeconds < 30 || req.RollbackSeconds > 900 {
			return decide(out, DecisionApprovalRequired, false, "network mutation requires staged application with bounded timed rollback",
				"set staged=true", "set rollback_seconds between 30 and 900"), nil
		}
		return decide(out, DecisionGuarded, true, "provider-owned staged network mutation has bounded rollback and acceptable control-plane blast radius",
			"verify end-to-end management before cutover"), nil

	case "recovery":
		return decide(out, DecisionGuarded, true, "recovery actions may proceed within bounded policy",
			"prefer actions that restore a verified management path"), nil
	default:
		return decide(out, DecisionApprovalRequired, false, "mutation kind has no dedicated control-plane safety policy",
			"add a bounded mutation policy before execution"), nil
	}
}

func recoveryEvidence(s Snapshot, req MutationRequest, blast BlastRadius) RecoveryEvidence {
	timedRollback := req.Staged && req.RollbackSeconds >= 30 && req.RollbackSeconds <= 900
	reversible := false
	recoveryAvailable := false
	switch req.Kind {
	case "read_only", "inspect", "status", "probe":
		reversible = true
		recoveryAvailable = true
	case "self_deploy":
		reversible = s.RollbackReady
		recoveryAvailable = s.RollbackReady && s.Primary != ""
	case "tunnel_restart":
		reversible = timedRollback
		recoveryAvailable = timedRollback && blast.VerifiedBackupExists
	case "network", "vpn", "dns", "routes", "firewall":
		reversible = timedRollback
		recoveryAvailable = timedRollback && (!blast.PrimaryAffected || blast.VerifiedBackupExists)
	case "recovery":
		reversible = s.RollbackReady || timedRollback
		recoveryAvailable = s.RollbackReady || blast.VerifiedBackupExists || timedRollback
	}
	return RecoveryEvidence{
		CurrentHealth:         s.EmergencyState,
		Reversible:            reversible,
		RecoveryPathAvailable: recoveryAvailable,
		RollbackReady:         s.RollbackReady,
		Staged:                req.Staged,
		RollbackSeconds:       req.RollbackSeconds,
	}
}

// managementPathAffected models known dependencies, not merely identical path
// IDs. Outbound MCP tunnels depend on this node's local MCP service; a change
// to local_loopback also affects them. Independent OOB paths do not.
func managementPathAffected(path Path, affected map[string]struct{}) bool {
	if _, ok := affected[path.ID]; ok {
		return true
	}
	if path.Kind == "overlay_ssh" {
		_, overlayAffected := affected["tailscale"]
		return overlayAffected
	}
	if path.Kind == "outbound_mcp_tunnel" {
		_, localAffected := affected["local_loopback"]
		return localAffected
	}
	return false
}

func blastRadius(s Snapshot, affected []string) BlastRadius {
	out := BlastRadius{AffectedPaths: append([]string(nil), affected...)}
	affectedSet := map[string]struct{}{}
	for _, id := range affected {
		affectedSet[id] = struct{}{}
	}
	if primary, found := findPath(s, s.Primary); found {
		out.PrimaryAffected = managementPathAffected(primary, affectedSet)
	}
	for _, p := range s.Paths {
		if p.ID == s.Primary || !p.External || !p.ManagementReady || !p.Verified {
			continue
		}
		if managementPathAffected(p, affectedSet) {
			continue
		}
		out.VerifiedBackupExists = true
		break
	}
	out.ControlLossPossible = out.PrimaryAffected && !out.VerifiedBackupExists
	return out
}

func inferredAffected(kind string) []string {
	switch kind {
	case "self_deploy", "agent_restart":
		return []string{"local_loopback", "openai_tunnel"}
	case "tunnel_restart":
		return []string{"openai_tunnel"}
	default:
		return nil
	}
}

func findPath(s Snapshot, id string) (Path, bool) {
	id = strings.TrimSpace(id)
	for _, p := range s.Paths {
		if p.ID == id {
			return p, true
		}
	}
	return Path{}, false
}
