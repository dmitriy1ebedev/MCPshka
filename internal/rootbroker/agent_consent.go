package rootbroker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"mcpshka/internal/roadmaprun"
)

// AgentConsent is approved with the existing Face ID challenge, never inferred
// from a model's launch arguments or a provider quota failure.
// Paths (0.15.0): up to four more repositories under the same Face ID.
type AgentConsent struct {
	Path   string   `json:"path"`
	Paths  []string `json:"paths,omitempty"`
	Agents []string `json:"agents"`
}

func (a *AgentConsent) Covers(path string) bool {
	return a != nil && (a.Path == path || slices.Contains(a.Paths, path))
}

func (a *AgentConsent) Validate() error {
	if a == nil {
		return nil
	}
	if len(a.Paths) > 4 || len(a.Agents) < 1 || len(a.Agents) > 4 {
		return errors.New("agent_consent requires one immediate repository (plus up to four in paths) and an explicit agent set")
	}
	seen, repos := map[string]bool{}, append([]string{a.Path}, a.Paths...)
	for i, s := range append(repos, a.Agents...) {
		repo := i < len(repos) && recipeRepository.MatchString(s) && filepath.Clean(s) == s && s != "/root/repos/mcpshka-controlled"
		if seen[s] || (i < len(repos) && !repo) || (i >= len(repos) && s != "codex" && s != "claude" && s != "codex_cloud" && s != "claude_cloud") {
			return errors.New("agent_consent needs unique immediate repositories and unique codex, claude, codex_cloud or claude_cloud agents")
		}
		seen[s] = true
	}
	return nil
}

func readAgentConsent(root string, grant AdminSession, now time.Time) (*AgentConsent, error) {
	if err := requireAdminSession(grant, grant.ID, grant.Owner, grant.Generation, now); err != nil {
		return nil, err
	}
	raw, err := readAdminRecord(filepath.Join(root, "passkey-"+grant.ID+".json"), 65536)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p passkeySession
	if json.Unmarshal(raw, &p) != nil {
		return nil, errors.New("invalid administrative challenge: HOLD")
	}
	if p.AgentConsent == nil {
		return nil, nil
	}
	issued, err := time.Parse(time.RFC3339Nano, grant.AuthorizedAt)
	if err != nil || p.Cancelled || p.ID != grant.ID || p.Owner != grant.Owner || p.Scope != adminSessionScope ||
		p.Policy != AdminSessionPolicy || p.PlanVersion != adminSessionPlanVersion || p.Generation+1 != grant.Generation ||
		p.Data.UserVerification != protocol.VerificationRequired || p.CreatedAt.IsZero() || issued.Before(p.CreatedAt) || issued.After(p.ExpiresAt) {
		return nil, errors.New("unconfirmed or superseded agent_consent: native_only")
	}
	if err := p.AgentConsent.Validate(); err != nil {
		return nil, err
	}
	return p.AgentConsent, nil
}

func requireAgentConsent(root string, grant AdminSession, path string, names []string, now time.Time) error {
	requested := &AgentConsent{Path: path, Agents: names}
	if err := requested.Validate(); err != nil {
		return err
	}
	approved, err := readAgentConsent(root, grant, now)
	if err != nil {
		return err
	}
	if approved == nil || !approved.Covers(path) {
		return errors.New("native_only: no owner-approved agent_consent for this repository")
	}
	for _, name := range names {
		found := false
		for _, allowed := range approved.Agents {
			if name == allowed {
				found = true
				break
			}
		}
		if !found {
			return errors.New("native_only: agent not explicitly approved by the owner")
		}
	}
	return nil
}

func checkAgentAdmission(root string, grant AdminSession, args []string, now time.Time) error {
	var path string
	var names []string
	switch args[0] {
	case "agent-task":
		if err := validateAgentTask(args); err != nil {
			return err
		}
		// Consent for a repository covers its own worktrees: a worktree task
		// carries its repository (validated shape), the runner re-checks it.
		path, names = agentTaskRepo(args), []string{args[4]}
	case "roadmap-run":
		if err := validateRoadmapRun(args); err != nil {
			return err
		}
		path = args[3]
		names, _ = roadmaprun.ParseAllowedAgents(args[len(args)-1])
	case "cloud-agent-start":
		if len(args) != 4 {
			return errors.New("invalid cloud agent admission")
		}
		var request struct {
			Provider string `json:"provider"`
		}
		if json.Unmarshal([]byte(args[3]), &request) != nil {
			return errors.New("invalid cloud agent admission")
		}
		path, names = "/root/repos/mcpshka", []string{request.Provider}
	case "agent-admission":
		if len(args) != 5 {
			return errors.New("agent-admission requires session, owner, repository and agent set")
		}
		path, names = args[3], strings.Split(args[4], ",")
	default:
		return nil
	}
	return requireAgentConsent(root, grant, path, names, now)
}
