package mcpserver

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/projectscaffold"
	"mcpshka/internal/repoadopt"
)

type ProjectCreateInput struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID window."`
	Name           string `json:"name" jsonschema:"[a-z][a-z0-9-]{1,39}; the project becomes /root/repos/<name>."`
	Kind           string `json:"kind" jsonschema:"go, python or generic."`
	IdempotencyKey string `json:"idempotency_key"`
}

type ProjectCreateResult struct {
	Path    string                    `json:"path"`
	Files   []string                  `json:"files"`
	Commit  string                    `json:"commit"`
	Profile projectprofile.Resolution `json:"profile"`
}

func registerProjectCreate(r *toolRegistry, profiles *projectprofile.Service) {
	addTool(r, &mcp.Tool{Name: "project_create", Description: "Create a new project end to end under the Face ID window: /root/repos/<name> owned by the service user (through the root broker), starting files (README, ROADMAP with «Gates:», a test, Makefile verify) for kind go/python/generic, git init with a first commit, and the registered profile. Refuses an existing path.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in ProjectCreateInput) (*mcp.CallToolResult, ProjectCreateResult, error) {
		out, err := runIdempotent(r.store, in.IdempotencyKey, "project_create", in, func() (ProjectCreateResult, error) {
			res := ProjectCreateResult{Path: filepath.Join("/root/repos", in.Name)}
			if !projectscaffold.Name.MatchString(in.Name) || projectscaffold.Kinds[in.Kind] == nil {
				return res, fmt.Errorf("name must match %s and kind be go, python or generic", projectscaffold.Name)
			}
			owner, err := adminOwner(ctx)
			if err != nil {
				return res, err
			}
			plan, err := runAdminSession[repoadopt.Plan](ctx, "project-adopt", in.SessionID, owner, "preview", res.Path, "-")
			if err != nil {
				return res, err
			}
			if !plan.Create {
				return res, fmt.Errorf("%s already exists", res.Path)
			}
			if _, err = runAdminSession[repoadopt.Plan](ctx, "project-adopt", in.SessionID, owner, "apply", res.Path, "-", plan.Digest); err != nil {
				return res, err
			}
			if res.Files, err = projectscaffold.Write(res.Path, in.Name, in.Kind); err != nil {
				return res, err
			}
			git := func(args ...string) (string, error) {
				cmd := exec.CommandContext(ctx, "git", append([]string{"-C", res.Path, "-c", "user.name=MCPshka", "-c", "user.email=mcpshka@localhost"}, args...)...)
				b, err := cmd.CombinedOutput()
				if err != nil {
					return "", fmt.Errorf("git %v: %v: %.300s", args, err, b)
				}
				return string(b), nil
			}
			for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "Каркас проекта (MCPshka project_create)"}} {
				if _, err = git(args...); err != nil {
					return res, err
				}
			}
			head, _ := git("rev-parse", "HEAD")
			res.Commit = strings.TrimSpace(head)
			proposed, err := profiles.Propose(res.Path)
			if err != nil {
				return res, err
			}
			res.Profile, err = profiles.Register(res.Path, proposed)
			return res, err
		})
		return nil, out, err
	})
}
