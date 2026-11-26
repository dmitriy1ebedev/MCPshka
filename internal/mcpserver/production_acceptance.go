package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/evidence"
	"mcpshka/internal/salvage"
	"mcpshka/internal/verify"
)

func (d *serverDeps) productionSource(ctx context.Context, in ProjectVerifyInput) (*verify.SourceProof, error) {
	status, err := d.deploys.Status(in.DeploymentID)
	if err != nil || status.State != "succeeded" || status.InstalledVersion != in.ExpectedVersion || status.ReconciliationRequired {
		return nil, errors.New("original deployment is not confirmed succeeded for the expected version: HOLD")
	}
	raw, err := brokerclient.Run(ctx, "release-proof", in.DeploymentID)
	if err != nil {
		return nil, err
	}
	var proof verify.SourceProof
	if len(raw) > 16384 || json.Unmarshal(raw, &proof) != nil {
		return nil, errors.New("invalid production proof")
	}
	raw, err = brokerclient.Run(ctx, "health", "broker")
	if err != nil {
		return nil, err
	}
	var broker struct {
		Version string
		SHA     string `json:"build_sha256"`
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &broker) != nil {
		return nil, errors.New("invalid broker identity")
	}
	running, err := capabilityBuild()
	if err != nil {
		return nil, err
	}
	if err = verify.BindSource(proof, in.DeploymentID, in.ExpectedVersion, gitRev(ctx, in.Path, "HEAD^{tree}"), running, broker.Version, broker.SHA); err != nil {
		return nil, err
	}
	return &proof, nil
}

// Cleanup proves idle scopes, including sibling worktrees; it never removes WIP.
func (d *serverDeps) releaseScopesIdle(ctx context.Context, root string) error {
	listing, err := salvage.Git(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	count := 0
	for _, field := range strings.Split(listing, "\x00") {
		path, ok := strings.CutPrefix(field, "worktree ")
		if !ok {
			continue
		}
		count++
		if count > 64 {
			return errors.New("worktree cleanup observation budget exceeded")
		}
		if d.leases == nil || d.pipelines == nil {
			return errors.New("workflow and lease authority unavailable")
		}
		lease, err := d.leases.Status(path)
		if err != nil {
			return err
		}
		if lease.Held {
			return errors.New("project writer lease remains")
		}
		claim, err := d.pipelines.ProjectClaim(path)
		if err != nil {
			return err
		}
		if claim != nil {
			return errors.New("workflow claim remains")
		}
		activity := d.manager.ProjectActivityReadOnly(path, 16)
		if activity.Count != 0 || activity.UnknownCount != 0 || activity.Truncated {
			return errors.New("project jobs require reconciliation before cleanup")
		}
	}
	if count == 0 {
		return errors.New("worktree cleanup authority unavailable")
	}
	pending, err := d.deploys.Pending()
	if err != nil {
		return err
	}
	if pending {
		return errors.New("deployment requires reconciliation before cleanup")
	}
	return nil
}

func (d *serverDeps) recordProductionSource(ctx context.Context, req *mcp.CallToolRequest, in ProjectVerifyInput, out *verify.Result) {
	proof, err := d.productionSource(ctx, in)
	check := verify.Check{Name: "release_provenance", OK: err == nil}
	if err != nil {
		check.Detail = err.Error()
		out.OK = false
	}
	out.Checks = append(out.Checks, check)
	if !out.OK {
		return
	}
	out.Source = proof
	inputs := evidence.Inputs{Head: gitRev(ctx, in.Path, "HEAD"), Tree: proof.Tree, Version: proof.Version, ArtifactSHA: proof.ControllerSHA256}
	recordGate(in.Path, "release", true, inputs, "project_verify", proof.CIReceiptSHA256)
	recordGate(in.Path, "deploy", true, inputs, "project_verify", in.DeploymentID)
	// An in-process call has no MCP peer and cannot prove connector acceptance.
	if req == nil || req.Session == nil {
		return
	}
	recordGate(in.Path, "acceptance", true, inputs, "project_verify_mcp", in.DeploymentID)
	cleanupErr := d.releaseScopesIdle(ctx, in.Path)
	cleanup := verify.Check{Name: "task_cleanup", OK: cleanupErr == nil}
	if cleanupErr != nil {
		cleanup.Detail = cleanupErr.Error()
	}
	out.Checks = append(out.Checks, cleanup)
	out.OK = out.OK && cleanup.OK
	recordGate(in.Path, "cleanup", cleanupErr == nil, inputs, "project_verify", in.DeploymentID)
}
