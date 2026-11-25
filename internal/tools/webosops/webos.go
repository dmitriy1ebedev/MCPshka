// Package webosops registers webos_deploy and webos_deploy_status.
package webosops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/evidence"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/approvedtask"
	"mcpshka/internal/webos"
)

type WebOSPairingStatusInput struct {
	TVIP string `json:"tv_ip,omitempty" jsonschema:"optional private IPv4 LG TV address for read-only TCP/9922 reachability, never SSH or key use"`
}

type WebOSDeployInput struct {
	Path           string `json:"path" jsonschema:"project with webos/install.py, e.g. /root/repos/free-my-tv"`
	IPK            string `json:"ipk" jsonschema:"repo-relative .ipk, e.g. out/webos/go2rtc-webos-pip-0.1.0.ipk: taken from the verified project_pipeline package export of HEAD (same artifact path), else from the repo; app id and version come from its control file"`
	AllowDowngrade bool   `json:"allow_downgrade,omitempty" jsonschema:"owner asked to install an older version over a newer one; pinned into the Face ID plan"`
	TVIP           string `json:"tv_ip" jsonschema:"TV IPv4 address (Developer Mode on, key already unlocked once)"`
	ID             string `json:"id,omitempty" jsonschema:"status: task id from webos_deploy"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type WebOSDeployResult struct {
	Task      rootbroker.ApprovedResult `json:"task"`
	Expected  webos.AppInfo             `json:"expected"`
	OnTV      *webos.AppInfo            `json:"on_tv,omitempty"`
	Confirmed bool                      `json:"confirmed"`
	Source    string                    `json:"source,omitempty"`
}

// Deps are the server hooks the tools need: the root broker's approved_task
// call and the acceptance gate recorder.
type Deps struct {
	ApprovedCall func(context.Context, ...string) (rootbroker.ApprovedResult, error)
	RecordGate   func(string, string, bool, evidence.Inputs, string, string)
}

func Register(add func(tooldef.Def), d Deps) {
	approvedCall, recordGate := d.ApprovedCall, d.RecordGate
	add(tooldef.New(&mcp.Tool{
		Name:        "webos_pairing_status",
		Description: "Read-only LG Developer Mode pairing status: key presence and safe permissions, pinned public SSH host fingerprint, optional private IPv4 TCP/9922 reachability. Does not read private key bytes, authenticate SSH, alter known_hosts or provision TV.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in WebOSPairingStatusInput) (*mcp.CallToolResult, webos.PairingStatus, error) {
		return nil, webos.InspectPairing(ctx, in.TVIP), nil
	}))
	add(tooldef.New(&mcp.Tool{Name: "webos_deploy", Description: "Install any webOS IPK of a project (e.g. the go2rtc PiP overlay) on an LG TV through the project's own installer as one approved_task: returns the Face ID approval_url (interpreter, inline code, installer, appinfo and IPK pinned by SHA-256). After the owner approves, run approved_task_execute with task.id, then webos_deploy_status confirms the version read back from the TV.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in WebOSDeployInput) (*mcp.CallToolResult, WebOSDeployResult, error) {
		var out WebOSDeployResult
		repo := filepath.Clean(in.Path)
		pkg, source, err := webos.Package(repo, in.IPK)
		if err != nil {
			return nil, out, err
		}
		plan, want, err := webos.PlanFrom(repo, in.TVIP, in.IPK, pkg, in.AllowDowngrade)
		if err != nil {
			return nil, out, err
		}
		if source != "repo file" {
			plan.Inputs[1] = "pipeline:" + in.IPK
		}
		plan, err = approvedtask.ResolveArtifacts(plan)
		if err != nil {
			return nil, out, err
		}
		if plan.Inputs[1] != pkg {
			return nil, out, fmt.Errorf("pipeline export changed while preparing webOS plan; retry with the same ipk")
		}
		out.Expected, out.Source = want, source
		sum := sha256.Sum256([]byte("webos-deploy\x00" + in.IdempotencyKey))
		raw, _ := json.Marshal(plan)
		out.Task, err = approvedCall(ctx, "prepare", hex.EncodeToString(sum[:16]), string(raw))
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "webos_deploy_status", Description: "State of a webos_deploy task; once DONE, confirmed=true only if the TV itself reports the expected app id and version (recorded as the device gate). Read-only.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in WebOSDeployInput) (*mcp.CallToolResult, WebOSDeployResult, error) {
		var out WebOSDeployResult
		var err error
		if out.Task, err = approvedCall(ctx, "status", in.ID); err != nil {
			return nil, out, err
		}
		// The approved plan pinned the IPK; its control file names the app.
		var pkg string
		for _, p := range out.Task.Plan.Inputs {
			if strings.HasSuffix(p, ".ipk") {
				pkg = p
			}
		}
		want, err := webos.IPKInfo(pkg)
		if err != nil {
			return nil, out, err
		}
		out.Expected = want
		if out.Task.State == "DONE" || out.Task.State == "FAILED" {
			got, ok := webos.Confirm(out.Task.Output, want)
			out.OnTV, out.Confirmed = &got, ok && out.Task.State == "DONE"
			recordGate(in.Path, "device", out.Confirmed, evidence.Inputs{Version: want.Version, Device: in.TVIP, ArtifactSHA: out.Task.Plan.InputSHAs[pkg]}, "webos_deploy", in.ID)
		}
		return nil, out, nil
	}))
}
