package rootbroker

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

func TestAgentAdmissionNativeOnlyAtRootBoundary(t *testing.T) {
	root, now := adminTestRoot(t), time.Now().UTC()
	grant := testAdminSession(now)
	if err := saveAdminSession(root, grant, now); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"agent-task", grant.ID, grant.Owner, "0123456789abcdef", "codex", "/root/repos/mcpshka"},
		{"roadmap-run", grant.ID, grant.Owner, "/root/repos/mcpshka", "1", "1", "agents:codex"},
		{"cloud-agent-start", grant.ID, grant.Owner, `{"provider":"codex_cloud"}`},
	} {
		lock, _, err := adminLegacyLeaseSessionAt(root, args, now)
		if lock != nil {
			lock.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "native_only") {
			t.Errorf("%s inherited delegation from a generic Face ID grant: %v", args[0], err)
		}
	}
	// Reconciliation and cancellation remain available, without launching a worker.
	for _, action := range []string{"cloud-agent-poll", "agent-task-cancel", "status-legacy-lease"} {
		lock, _, err := adminLegacyLeaseSessionAt(root, []string{action, grant.ID, grant.Owner}, now)
		if lock != nil {
			lock.Close()
		}
		if err != nil {
			t.Errorf("%s blocked by native_only: %v", action, err)
		}
	}
}

func TestAgentAdmissionUsesRootPinnedOwnerScope(t *testing.T) {
	root, now := adminTestRoot(t), time.Now().UTC()
	grant := testAdminSession(now.Add(-2 * time.Minute))
	if err := saveAdminSession(root, grant, now); err != nil {
		t.Fatal(err)
	}
	challenge := passkeySession{ID: grant.ID, Owner: grant.Owner, Scope: adminSessionScope,
		Policy: AdminSessionPolicy, PlanVersion: adminSessionPlanVersion, Generation: grant.Generation - 1,
		CreatedAt: now.Add(-3 * time.Minute), ExpiresAt: now.Add(-time.Minute),
		Data: webauthn.SessionData{UserVerification: protocol.VerificationRequired}}
	raw, _ := json.Marshal(challenge)
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record["agent_consent"] = map[string]any{"path": "/root/repos/mcpshka", "agents": []string{"codex"}}
	if err := writeRootOnly(filepath.Join(root, "passkey-"+grant.ID+".json"), record, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, agents string
		allowed            bool
	}{
		{"approved", "/root/repos/mcpshka", "codex", true},
		{"other_agent", "/root/repos/mcpshka", "claude", false},
		{"quota_fallback", "/root/repos/mcpshka", "codex,claude", false},
		{"other_project", "/root/repos/diag", "codex", false},
		{"sibling_scope", "/root/repos/mcpshka-forge", "codex", false},
		{"own_worktree", "/root/repos/wt/mcpshka-codex /root/repos/mcpshka", "codex", true},
		{"other_worktree", "/root/repos/wt/diag-codex /root/repos/diag", "codex", false},
		{"worktree_as_repo", "/root/repos/wt/x /root/repos/wt/y", "codex", false},
	} {
		args := []string{"roadmap-run", grant.ID, grant.Owner, tc.path, "1", "1", "agents:" + tc.agents}
		if wt, repo, ok := strings.Cut(tc.path, " "); ok {
			args = []string{"agent-task", grant.ID, grant.Owner, "0123456789abcdef", tc.agents, wt, repo}
		}
		lock, _, err := adminLegacyLeaseSessionAt(root, args, now)
		if lock != nil {
			lock.Close()
		}
		if (err == nil) != tc.allowed {
			t.Errorf("%s: allowed=%v err=%v", tc.name, tc.allowed, err)
		}
	}
	record["generation"] = grant.Generation
	if err := writeRootOnly(filepath.Join(root, "passkey-"+grant.ID+".json"), record, false); err != nil {
		t.Fatal(err)
	}
	lock, _, err := adminLegacyLeaseSessionAt(root, []string{"agent-task", grant.ID, grant.Owner, "0123456789abcdef", "codex", "/root/repos/mcpshka"}, now)
	if lock != nil {
		lock.Close()
	}
	if err == nil {
		t.Fatal("unconfirmed challenge generation authorized an agent")
	}
}

func TestAgentConsentRejectsUnconfirmedRootRecords(t *testing.T) {
	root, now := adminTestRoot(t), time.Now().UTC()
	grant := testAdminSession(now.Add(-2 * time.Minute))
	p := passkeySession{ID: grant.ID, Owner: grant.Owner, Scope: adminSessionScope, Policy: AdminSessionPolicy,
		PlanVersion: adminSessionPlanVersion, Generation: grant.Generation - 1, CreatedAt: now.Add(-3 * time.Minute), ExpiresAt: now.Add(-time.Minute),
		Data:         webauthn.SessionData{UserVerification: protocol.VerificationRequired},
		AgentConsent: &AgentConsent{Path: "/root/repos/mcpshka", Agents: []string{"codex"}}}
	for name, tamper := range map[string]func(*passkeySession){
		"owner":              func(p *passkeySession) { p.Owner = "other-owner" },
		"generation":         func(p *passkeySession) { p.Generation++ },
		"cancelled":          func(p *passkeySession) { p.Cancelled = true },
		"scope":              func(p *passkeySession) { p.Scope = "approved_task" },
		"policy":             func(p *passkeySession) { p.Policy = "other" },
		"unverified":         func(p *passkeySession) { p.Data.UserVerification = protocol.VerificationPreferred },
		"after_expiry":       func(p *passkeySession) { p.ExpiresAt = now.Add(-4 * time.Minute) },
		"before_preparation": func(p *passkeySession) { p.CreatedAt = now.Add(-time.Minute) },
	} {
		bad := p
		tamper(&bad)
		if err := writeRootOnly(filepath.Join(root, "passkey-"+grant.ID+".json"), bad, false); err != nil {
			t.Fatal(err)
		}
		if err := requireAgentConsent(root, grant, "/root/repos/mcpshka", []string{"codex"}, now); err == nil {
			t.Fatal("tampered consent admitted", name)
		}
	}
	if err := saveAdminSession(root, grant, now); err != nil {
		t.Fatal(err)
	}
	raw, err := readAdminRecord(filepath.Join(root, adminSessionFile), 65536)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 8 {
		t.Fatal("0.8.1 grant shape changed", string(raw), err)
	}
}

func TestAgentConsentScopeValidation(t *testing.T) {
	for _, a := range []*AgentConsent{
		{Path: "/root/repos/mcpshka", Agents: nil},
		{Path: "/root/repos/mcpshka", Agents: []string{"codex", "codex"}},
		{Path: "/root/repos/mcpshka", Agents: []string{"unknown"}},
		{Path: "/root/repos/../diag", Agents: []string{"codex"}},
		{Path: "/etc", Agents: []string{"codex"}},
		{Path: "/root/repos/mcpshka-controlled", Agents: []string{"codex"}},
	} {
		if err := a.Validate(); err == nil {
			t.Fatal("unbounded agent scope", a)
		}
	}
	for _, name := range []string{"codex", "claude", "codex_cloud", "claude_cloud"} {
		if err := (&AgentConsent{Path: "/root/repos/mcpshka", Agents: []string{name}}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
