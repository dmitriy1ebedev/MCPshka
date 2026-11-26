// Package gitcontrol binds provider APIs and a bounded Git worker to durable
// receipts. MCP bindings live in mcpserver; the operation is independent of it.
package gitcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mcpshka/internal/forgeci"
	"mcpshka/internal/gitprovider"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"path/filepath"
	"strings"
	"time"
)

type Auth func(context.Context, string) error
type Principal func(context.Context) (string, error)
type QueryInput struct {
	Provider string `json:"provider" jsonschema:"github, gitverse or moshub."`
	Action   string `json:"action" jsonschema:"capabilities, whoami, list, get, branches or ci_runs."`
	Owner    string `json:"owner,omitempty"`
	Repo     string `json:"repo,omitempty"`
}
type ChangeInput struct {
	Provider       string         `json:"provider" jsonschema:"github, gitverse or moshub."`
	Action         string         `json:"action" jsonschema:"create, update, collaborator or fork (GitHub only)."`
	Owner          string         `json:"owner"`
	Repo           string         `json:"repo"`
	ParentOwner    string         `json:"parent_owner,omitempty"`
	ParentRepo     string         `json:"parent_repo,omitempty"`
	Description    string         `json:"description,omitempty"`
	Private        *bool          `json:"private,omitempty"`
	Changes        map[string]any `json:"changes,omitempty"`
	Collaborator   string         `json:"collaborator,omitempty"`
	Permission     string         `json:"permission,omitempty"`
	AdminSessionID string         `json:"admin_session_id"`
	IdempotencyKey string         `json:"idempotency_key"`
}
type PushInput struct {
	Provider       string `json:"provider"`
	Owner          string `json:"owner"`
	Repo           string `json:"repo"`
	Path           string `json:"path"`
	LeaseToken     string `json:"lease_token"`
	AdminSessionID string `json:"admin_session_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type ChangeResult struct {
	Action   string `json:"action"`
	Provider string `json:"provider"`
	State    string `json:"state"`
	Result   any    `json:"result,omitempty"`
}

func Query(ctx context.Context, in QueryInput) (map[string]any, error) {
	c, err := gitprovider.New(gitprovider.Site(in.Provider))
	if err != nil {
		return nil, err
	}
	switch in.Action {
	case "capabilities":
		return map[string]any{"capabilities": c.Capability()}, nil
	case "whoami":
		name, err := c.WhoAmI(ctx)
		return map[string]any{"provider": in.Provider, "login": name}, err
	case "list":
		repos, err := c.List(ctx)
		return map[string]any{"provider": in.Provider, "repositories": repos}, err
	case "get":
		repo, err := c.Get(ctx, in.Owner, in.Repo)
		return map[string]any{"provider": in.Provider, "repository": repo}, err
	case "branches":
		branches, err := c.Branches(ctx, in.Owner, in.Repo)
		return map[string]any{"provider": in.Provider, "branches": branches}, err
	case "ci_runs":
		runs, err := c.CIRuns(ctx, in.Owner, in.Repo)
		return map[string]any{"provider": in.Provider, "runs": runs}, err
	default:
		return nil, gitprovider.ErrUnsupported
	}
}
func validKey(key string) bool {
	return key != "" && len(key) <= 180 && !strings.ContainsAny(key, "\r\n\x00")
}
func Change(ctx context.Context, in ChangeInput, db *idempotency.Store, authenticate Auth, who Principal) (ChangeResult, error) {
	if !validKey(in.IdempotencyKey) {
		return ChangeResult{}, errors.New("idempotency key required")
	}
	if err := authenticate(ctx, in.AdminSessionID); err != nil {
		return ChangeResult{}, err
	}
	c, err := gitprovider.New(gitprovider.Site(in.Provider))
	if err != nil {
		return ChangeResult{}, err
	}
	switch in.Action {
	case "create":
		if in.Private == nil {
			return ChangeResult{}, errors.New("private must be explicit")
		}
	case "fork":
		if in.Provider != "github" || in.ParentOwner == "" || in.ParentRepo == "" || in.Private == nil || *in.Private {
			return ChangeResult{}, errors.New("GitHub fork requires upstream parent and explicit public visibility")
		}
	case "update":
		if len(in.Changes) == 0 {
			return ChangeResult{}, errors.New("update fields required")
		}
	case "collaborator":
		if in.Collaborator == "" || in.Permission == "" {
			return ChangeResult{}, errors.New("collaborator and permission required")
		}
	default:
		return ChangeResult{}, gitprovider.ErrUnsupported
	}
	owner, err := who(ctx)
	if err != nil {
		return ChangeResult{}, err
	}
	return idempotency.Run(db, "git-provider:"+owner+":"+in.IdempotencyKey, "git_provider_change", in, func() (ChangeResult, error) {
		result := ChangeResult{Action: in.Action, Provider: in.Provider}
		switch in.Action {
		case "fork":
			repo, e := c.Fork(ctx, in.ParentOwner, in.ParentRepo, in.Owner, in.Repo)
			result.State = repo.State
			result.Result = repo
			return result, e
		case "create":
			repo, e := c.Create(ctx, in.Owner, in.Repo, in.Description, *in.Private)
			result.State = repo.State
			result.Result = repo
			return result, e
		case "update":
			repo, e := c.Update(ctx, in.Owner, in.Repo, in.Changes)
			result.State = repo.State
			result.Result = repo
			return result, e
		case "collaborator":
			e := c.Collaborator(ctx, in.Owner, in.Repo, in.Collaborator, in.Permission)
			// PUT is accepted but no independent GET proving access yet.
			result.State = "SUBMITTED_UNVERIFIED"
			return result, e
		}
		return result, fmt.Errorf("unsupported Git provider action")
	})
}
func Push(ctx context.Context, in PushInput, m *jobs.Manager, db *idempotency.Store, authenticate Auth, who Principal) (jobs.Snapshot, error) {
	if !validKey(in.IdempotencyKey) || strings.TrimSpace(in.LeaseToken) == "" {
		return jobs.Snapshot{}, errors.New("idempotency key and lease required")
	}
	if err := authenticate(ctx, in.AdminSessionID); err != nil {
		return jobs.Snapshot{}, err
	}
	switch in.Provider {
	case "github", "gitverse", "moshub":
	default:
		return jobs.Snapshot{}, errors.New("invalid git provider")
	}
	if in.Owner == "" || in.Repo == "" || in.Path == "" {
		return jobs.Snapshot{}, errors.New("exact remote and path required")
	}
	if len(in.Owner) > 100 || len(in.Repo) > 100 {
		return jobs.Snapshot{}, errors.New("remote identity length exceeds limit")
	}
	root, err := m.Policy().ResolveExisting(in.Path)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	if filepath.Clean(in.Path) != root {
		return jobs.Snapshot{}, errors.New("path must be canonical")
	}
	actor, err := who(ctx)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	key := "git-provider-push:" + actor + ":" + in.IdempotencyKey
	payload := in
	sum := sha256.Sum256([]byte(in.LeaseToken))
	payload.LeaseToken = hex.EncodeToString(sum[:])
	return idempotency.Run(db, key, "git_provider_push", payload, func() (jobs.Snapshot, error) {
		args := []string{"git-provider", "push", in.Provider, in.Owner, in.Repo, root}
		return m.StartWithOptions(forgeci.Command, args, root, jobs.StartOptions{
			HolderToken: in.LeaseToken, Timeout: 5 * time.Minute, OutputLimitBytes: 8192, MemoryLimitBytes: 256 << 20,
			CPUTimeLimit: 180 * time.Second, CPUQuotaPercent: 100, ProcessLimit: 32, PrivateTemp: true,
		})
	})
}
