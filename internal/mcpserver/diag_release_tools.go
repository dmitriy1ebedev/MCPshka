package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/diagprovider"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectrelease"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/unitbus"
)

var (
	diagReleaseVersion = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`)
	diagReleaseResults = "/var/lib/mcpshka-diag-release"
)

type DiagReleaseApplyInput struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID window."`
	Commit         string `json:"commit" jsonschema:"Full 40-hex DIAG commit on DIAG main (accepted phase)."`
	Version        string `json:"version" jsonschema:"DIAG version the commit declares in internal/version/version.go, for example 0.1.11."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
}

type DiagReleaseStatusInput struct {
	Version string `json:"version" jsonschema:"Version passed to diag_release_apply."`
}

type DiagReleaseStatusOutput struct {
	rootbroker.DiagReleaseResult
	UnitState string                    `json:"unit_state"`
	Health    diagprovider.NativeHealth `json:"health"`
}

func registerDiagReleaseTools(r *toolRegistry, m *jobs.Manager) {
	closed, destructive := false, false
	addTool(r, &mcp.Tool{
		Name:        "diag_release_apply",
		Description: "Install a DIAG build on the host under the Face ID window instead of copying binaries by hand: root takes the commit only from DIAG main, extracts it (regular files only), builds diag and diagd offline with the declared version, smokes the new diag as nobody (version, capabilities schema), swaps both keeping .previous, restarts diagd and rolls back if the installed pair does not answer. Returns STARTED at once; poll diag_release_status.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DiagReleaseApplyInput) (*mcp.CallToolResult, rootbroker.DiagReleaseResult, error) {
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, rootbroker.DiagReleaseResult{}, err
		}
		if len(in.Commit) != 40 || !diagReleaseVersion.MatchString(in.Version) {
			return nil, rootbroker.DiagReleaseResult{}, errors.New("full 40-hex commit and a version like 0.1.11 are required")
		}
		out, err := runIdempotent(r.store, in.IdempotencyKey, "diag_release_apply", in, func() (rootbroker.DiagReleaseResult, error) {
			return runAdminSession[rootbroker.DiagReleaseResult](ctx, "diag-release", in.SessionID, owner, strings.ToLower(in.Commit), in.Version)
		})
		return nil, out, err
	})
	native := diagprovider.NewNative(m)
	addTool(r, &mcp.Tool{
		Name:        "diag_release_status",
		Description: "Result of a diag_release_apply: INSTALLED, ROLLED_BACK, FAILED (with the reason) or STARTED while the root unit builds, plus the worker unit state and live diag_health of the installed DIAG. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in DiagReleaseStatusInput) (*mcp.CallToolResult, DiagReleaseStatusOutput, error) {
		if !diagReleaseVersion.MatchString(in.Version) {
			return nil, DiagReleaseStatusOutput{}, errors.New("version like 0.1.11 is required")
		}
		out := DiagReleaseStatusOutput{UnitState: "unknown"}
		out.Version, out.Unit, out.State = in.Version, diagprovider.ReleaseUnit(in.Version), "NOT_STARTED"
		show, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if raw, err := unitbus.Output(show, "show", "--property=ActiveState,Result", "--value", out.Unit+".service"); err == nil {
			out.UnitState = strings.Join(strings.Fields(string(raw)), "/")
			if strings.HasPrefix(out.UnitState, "activ") {
				out.State = "STARTED"
			}
		}
		if raw, err := os.ReadFile(filepath.Join(diagReleaseResults, in.Version+".json")); err == nil && len(raw) < 16<<10 {
			var res rootbroker.DiagReleaseResult
			if json.Unmarshal(raw, &res) == nil && res.Version == in.Version {
				out.DiagReleaseResult = res
			}
		}
		out.Health, _ = native.Health(ctx)
		return nil, out, nil
	})
}

// project_release is the same path for any project (MCP-171); DIAG moves to
// it once it carries a recipe.
func registerProjectRelease(r *toolRegistry) {
	closed, destructive := false, false
	addTool(r, &mcp.Tool{Name: "project_release", Description: projectrelease.ApplyDescription, Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in projectrelease.Input) (*mcp.CallToolResult, projectrelease.Result, error) {
		owner, err := adminOwner(ctx)
		args, aerr := in.BrokerArgs(owner)
		if err = errors.Join(err, aerr); err != nil {
			return nil, projectrelease.Result{}, err
		}
		out, err := runIdempotent(r.store, in.IdempotencyKey, "project_release", in, func() (projectrelease.Result, error) {
			return runAdminSession[projectrelease.Result](ctx, args...)
		})
		return nil, out, err
	})
	addTool(r, &mcp.Tool{Name: "project_release_status", Description: projectrelease.StatusDescription, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in projectrelease.StatusInput) (*mcp.CallToolResult, projectrelease.Result, error) {
		out, err := projectrelease.Status(ctx, in)
		return nil, out, err
	})
}
