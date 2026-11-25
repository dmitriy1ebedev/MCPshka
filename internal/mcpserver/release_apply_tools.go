package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/releaseapplystatus"
	"mcpshka/internal/releasequeue"
	"mcpshka/internal/statestore"
	"mcpshka/internal/unitbus"
)

var releaseApplyVersion = regexp.MustCompile(`^0\.[0-9]{1,4}\.[0-9]{1,6}$`)

type ReleaseApplyInput struct {
	SessionID      string `json:"admin_session_id" jsonschema:"Active Face ID window."`
	Version        string `json:"version" jsonschema:"Release version, for example 0.1.173; the commit's server.go must declare it."`
	StoreID        string `json:"store_id" jsonschema:"Protected repository store ID (32 hex); its main must be the built commit."`
	BuildPermit    string `json:"build_permit,omitempty" jsonschema:"Install now: permit_id from release_build_prepare (sealed, successful). Not used by queue/cancel."`
	TestPermit     string `json:"test_permit,omitempty" jsonschema:"Install now: fenced successful module or GitVerse test permit of the same commit. Not used by queue/cancel."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key."`
	Action         string `json:"action,omitempty" jsonschema:"Empty: install now. queue: autorelease branch+commit (the server drives CI, ship, build, install, verify itself in your Face ID window). cancel: stop a queued autorelease before install."`
	Branch         string `json:"branch,omitempty" jsonschema:"With action=queue: branch holding the commit."`
	Commit         string `json:"commit,omitempty" jsonschema:"With action=queue: full 40-hex commit."`
	ShipItem       string `json:"ship_item,omitempty" jsonschema:"With action=queue: integration_ship id that already shipped this commit by hand, with its original branch and evidence; the queue continues it."`
	Evidence       string `json:"evidence,omitempty" jsonschema:"With action=queue: integration_ship evidence, auto (default), ci, gitverse, distributed or module."`
}

type ReleaseApplyOutput struct {
	Queue       *releasequeue.View `json:"queue,omitempty"`
	Version     string             `json:"version"`
	Commit      string             `json:"commit"`
	Controller  string             `json:"controller_sha256"`
	Broker      string             `json:"broker_sha256"`
	TestKind    string             `json:"test_evidence"`
	OperationID string             `json:"operation_id"`
	Unit        string             `json:"unit"`
	State       string             `json:"state"`
	Reason      string             `json:"reason,omitempty"`
}

type ReleaseApplyStatusInput struct {
	Version string `json:"version" jsonschema:"Version passed to release_apply."`
}

type ReleaseApplyStatusOutput = releaseapplystatus.Output

func releaseApplyUnitName(version string) string {
	return "mcpshka-release-apply-" + strings.ReplaceAll(version, ".", "")
}

func installedVersion(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8765/healthz", nil)
	if err != nil {
		return ""
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	var h struct {
		Version string `json:"version"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&h) != nil {
		return ""
	}
	return h.Version
}

var releaseInstalledVersion = installedVersion
var releaseUnitOutput = unitbus.Output
var releaseRootStatus = func(ctx context.Context, version string) ([]byte, error) {
	return brokerclient.Run(ctx, "release-apply-status", version)
}

func registerReleaseApplyTools(r *toolRegistry, db *statestore.DB) {
	closed, destructive := false, false
	addTool(r, &mcp.Tool{
		Name:        "release_apply",
		Description: "Install a release under the Face ID window without the owner's SSH apply: root checks the sealed local release receipt (build + fenced test of one commit), that the commit is the protected store's main and declares version, re-hashes the root-owned images, stages candidates, helper and plan from that commit, runs coordinated-upgrade --check and starts --apply in its own unit (it restarts MCPshka and rolls both binaries back if health fails). Returns APPLYING; follow with release_apply_status after the restart. action=queue «выпусти сам» (version, branch, commit, store_id): a watcher in the server then drives integration_ship, release_build_prepare, release_local, this install and project_verify itself, as you and only in your live Face ID window (it waits with a reminder otherwise); action=cancel stops it before install; progress is release_apply_status.queue.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ReleaseApplyInput) (*mcp.CallToolResult, ReleaseApplyOutput, error) {
		owner, err := adminOwner(ctx)
		if err != nil {
			return nil, ReleaseApplyOutput{}, err
		}
		if !releaseApplyVersion.MatchString(in.Version) {
			return nil, ReleaseApplyOutput{}, errors.New("version must look like 0.1.173")
		}
		if in.Action != "" {
			identity, _ := authn.IdentityFromContext(ctx)
			q, err := runIdempotent(r.store, in.IdempotencyKey, "release_queue", in, func() (releasequeue.View, error) {
				return releasequeue.Mutate(db, identity, releasequeue.Input{Action: in.Action, Version: in.Version, Branch: in.Branch, Commit: in.Commit, StoreID: in.StoreID, ShipItem: in.ShipItem, Evidence: in.Evidence}, time.Now().UTC())
			})
			return nil, ReleaseApplyOutput{Version: in.Version, Commit: q.Commit, State: q.Stage, Queue: &q}, err
		}
		out, err := runIdempotent(r.store, in.IdempotencyKey, "release_apply", in, func() (ReleaseApplyOutput, error) {
			return runAdminSession[ReleaseApplyOutput](ctx, "release-apply", in.SessionID, owner, in.Version, in.StoreID, in.BuildPermit, in.TestPermit)
		})
		return nil, out, err
	})
	addTool(r, &mcp.Tool{
		Name:        "release_apply_status",
		Description: "State of a release_apply: installed version from /healthz and the apply unit's systemd state. INSTALLED only with successful root audit and matching running version; HOLD includes the root failure and traceback; queue is the autorelease of this version (stage, detail, history, chain commit → CI permit → candidate → installed → rollback); stages keeps implementation_complete, integrated, production_verified and accepted apart. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ReleaseApplyStatusInput) (*mcp.CallToolResult, ReleaseApplyStatusOutput, error) {
		if !releaseApplyVersion.MatchString(in.Version) {
			return nil, ReleaseApplyStatusOutput{}, errors.New("version must look like 0.1.173")
		}
		out := ReleaseApplyStatusOutput{Version: in.Version, Unit: releaseApplyUnitName(in.Version), Installed: releaseInstalledVersion(ctx), UnitState: "unknown"}
		show, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if raw, err := releaseUnitOutput(show, "show", "--property=ActiveState,Result", "--value", out.Unit+".service"); err == nil {
			out.UnitState = strings.Join(strings.Fields(string(raw)), "/")
		}
		if q, err := releasequeue.Read(ctx, db, releasequeue.StatusInput{Version: in.Version}); err == nil && len(q.Releases) > 0 {
			out.Queue = &q.Releases[0]
		}
		out.Stages = releasequeue.StagesOf(out.Queue, out.Installed, in.Version)
		switch {
		case out.Installed == in.Version:
			out.State = "INSTALLED"
		case strings.HasPrefix(out.UnitState, "active") || strings.HasPrefix(out.UnitState, "activating"):
			out.State = "APPLYING"
		case strings.Contains(out.UnitState, "failed") || strings.HasSuffix(out.UnitState, "/exit-code"):
			out.State = "FAILED"
		default:
			out.State = "NOT_STARTED"
		}
		rootCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		raw, rootErr := releaseRootStatus(rootCtx, in.Version)
		stop()
		releaseapplystatus.Reconcile(&out, raw, rootErr)
		out.Progress = releasequeue.ProgressOf(out.Queue, out.State, time.Now().UTC())
		return nil, out, nil
	})
}
