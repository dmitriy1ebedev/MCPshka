package releaseops

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/toolcontract"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/adminops"
	"mcpshka/internal/tools/integration"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/releaseprep"
)

const Root = "/root/repos/mcpshka"
const Runner = Root + "/packaging/native/release-artifact.py"

type ReleasePrepareInput struct {
	Source         string         `json:"source,omitempty"`
	BuildPermit    string         `json:"build_permit,omitempty"`
	LaunchID       string         `json:"launch_id,omitempty"`
	TestPermit     string         `json:"test_permit,omitempty"`
	ArtifactURL    string         `json:"artifact_url,omitempty"`
	ArtifactPath   string         `json:"artifact_path,omitempty"`
	Version        string         `json:"version"`
	CurrentVersion string         `json:"current_version"`
	SourceDigest   string         `json:"source_digest"`
	Receipt        map[string]any `json:"receipt,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
}
type ReleaseStatusInput struct {
	JobID string `json:"job_id"`
}
type ReleaseStatus struct {
	JobID    string         `json:"job_id"`
	State    jobs.State     `json:"state"`
	ExitCode *int           `json:"exit_code,omitempty"`
	Error    string         `json:"error,omitempty"`
	Prepared map[string]any `json:"prepared,omitempty"`
	Trust    string         `json:"trust"`
}

func JobInput(in ReleasePrepareInput) ([]string, jobs.StartOptions, error) {
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return nil, jobs.StartOptions{}, errors.New("release_prepare requires retry key")
	}
	if in.Source == "local" {
		// A job is a separate process and the root broker accepts only the
		// controller's own PID: local releases run step by step in-process.
		return nil, jobs.StartOptions{}, errors.New("source=local runs through release_local (one step per call), not a job")
	}
	if (in.Source != "" && in.Source != "github") || in.BuildPermit != "" || in.LaunchID != "" || in.TestPermit != "" {
		return nil, jobs.StartOptions{}, errors.New("invalid release source")
	}
	if (in.ArtifactURL == "") == (in.ArtifactPath == "") {
		return nil, jobs.StartOptions{}, errors.New("exactly one release artifact source required")
	}
	env := map[string]string{"GOMAXPROCS": "1", "GOMEMLIMIT": "160MiB"}
	if in.ArtifactURL != "" {
		u, e := url.Parse(in.ArtifactURL)
		if e != nil || len(in.ArtifactURL) > 8192 || strings.ContainsAny(in.ArtifactURL, "\r\n\t ") || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !strings.HasSuffix(u.Hostname(), ".oaiusercontent.com") || (u.Port() != "" && u.Port() != "443") {
			return nil, jobs.StartOptions{}, errors.New("release artifact URL rejected")
		}
		env["MCPSHKA_RELEASE_URL"] = in.ArtifactURL
	} else {
		const prefix = "/var/lib/mcpshka/artifact-inbox/"
		name := strings.TrimPrefix(in.ArtifactPath, prefix)
		if !strings.HasPrefix(in.ArtifactPath, prefix) || len(name) != 37 || !strings.HasSuffix(name, ".blob") {
			return nil, jobs.StartOptions{}, errors.New("release artifact inbox path rejected")
		}
		for _, c := range name[:32] {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return nil, jobs.StartOptions{}, errors.New("release artifact inbox ID rejected")
			}
		}
		env["MCPSHKA_RELEASE_FILE"] = in.ArtifactPath
	}
	payload := map[string]any{"version": in.Version, "current_version": in.CurrentVersion, "source_digest": in.SourceDigest, "receipt": in.Receipt}
	raw, e := json.Marshal(payload)
	if e != nil || len(raw) > 8192 {
		return nil, jobs.StartOptions{}, errors.New("invalid bounded release metadata")
	}
	return []string{"release-artifact", string(raw)}, jobs.StartOptions{
		Timeout: 120 * time.Second, OutputLimitBytes: 8192, MemoryLimitBytes: 256 << 20,
		CPUTimeLimit: 60 * time.Second, CPUQuotaPercent: 100, ProcessLimit: 8, PrivateTemp: true,
		Env: env,
	}, nil
}
func Start(m *jobs.Manager, store *idempotency.Store, in ReleasePrepareInput) (jobs.Snapshot, error) {
	args, opts, e := JobInput(in)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	return idempotency.Run(store, in.IdempotencyKey, "release_prepare", in, func() (jobs.Snapshot, error) {
		return m.StartWithOptions(releaseprep.Command, args, Root, opts)
	})
}
func Read(m *jobs.Manager, id string) (ReleaseStatus, error) {
	s, e := m.Get(id)
	if e != nil {
		return ReleaseStatus{}, e
	}
	legacy := filepath.Base(s.Command) == "python3" && len(s.Args) == 3 && s.Args[0] == "-I" && s.Args[1] == Runner
	native := s.Command == releaseprep.Command && len(s.Args) == 2 && s.Args[0] == "release-artifact"
	if !legacy && !native {
		return ReleaseStatus{}, errors.New("job is not release preparation")
	}
	out := ReleaseStatus{JobID: id, State: s.State, ExitCode: s.ExitCode, Error: s.Error, Trust: "UNTRUSTED"}
	if s.ExitCode != nil && *s.ExitCode == 0 {
		raw, e := m.Read(id, "stdout", 0, 8192)
		if e != nil {
			return out, e
		}
		if raw.Truncated || json.Unmarshal([]byte(raw.Text), &out.Prepared) != nil || out.Prepared["schema"] != "mcpshka.release-prepared/v1" {
			return out, errors.New("release result missing or invalid; reconcile candidate")
		}
	} else if s.ExitCode != nil {
		out.Error = "preparation failed; inspect bounded job output and candidate; do not replay UNKNOWN"
	}
	return out, nil
}

type ReleaseLocalInput struct {
	Version        string `json:"version"`
	CurrentVersion string `json:"current_version"`
	SourceDigest   string `json:"source_digest" jsonschema:"manifest.source.release_digest from release_build_prepare."`
	BuildPermit    string `json:"build_permit" jsonschema:"permit_id from release_build_prepare."`
	LaunchID       string `json:"launch_id" jsonschema:"launch_id from release_build_prepare."`
	TestPermit     string `json:"test_permit" jsonschema:"Fenced successful module or GitVerse test permit of the same commit."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"A new key per call (each call is one step)."`
}

func Register(add func(tooldef.Def), m *jobs.Manager, store *idempotency.Store, owner func(context.Context) string) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "release_local", Description: "Prepare a paired candidate from a sealed local build without GitHub, one step per call: launch the release_build_prepare permit, wait (BUILD_RUNNING), fence it, then fetch the root-sealed receipt and both images and stage candidate, broker, plan, receipt and coordinated manifest in bin/ (STAGED, prepared metadata). Use a new idempotency_key per call. No installation. Once BUILD_FENCING has passed, release_apply can install directly (it re-hashes the images itself); COLLECTING reports progress and next_action. Otherwise promote the STAGED candidate with project_deploy.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ReleaseLocalInput) (*mcp.CallToolResult, releaseprep.LocalProgress, error) {
		raw, err := json.Marshal(releaseprep.LocalRequest{Source: "local", Version: in.Version, CurrentVersion: in.CurrentVersion, SourceDigest: in.SourceDigest, BuildPermit: in.BuildPermit, LaunchID: in.LaunchID, TestPermit: in.TestPermit})
		if err != nil {
			return nil, releaseprep.LocalProgress{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		out, err := releaseprep.StepLocal(ctx, raw)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "release_build_prepare", Description: "Seal an offline paired ARM64 build from an immutable commit under the active Face ID administrative session. Preparation waits in the host heavy-task queue under load; QUEUED includes reason and position. Does not launch or deploy. Poll with a new retry key until SEALED; pass permit_id, launch_id and manifest.source.release_digest to release_local with the fenced successful gitverse/module test permit of the same commit.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, _ *mcp.CallToolRequest, in ReleaseBuildPrepareInput) (*mcp.CallToolResult, map[string]any, error) {
		owner, e := adminops.Owner(ctx)
		if e != nil {
			return nil, nil, e
		}
		if in.Attempt < 0 || in.Attempt > 9 {
			return nil, nil, errors.New("release build attempt must be 1-9")
		}
		args := []string{"release-build-prepare", in.SessionID, owner, in.Path, in.Commit}
		if in.Attempt > 1 {
			args = append(args, strconv.Itoa(in.Attempt))
		}
		out, e := adminops.Run[map[string]any](ctx, args...)
		return nil, out, e
	}))
	add(tooldef.New(&mcp.Tool{Name: "release_prepare", Description: "Prepare a paired candidate without deployment. source=local launches a release_build_prepare sealed permit, fences its result and consumes a successful gitverse/module test_permit; source=github (default) uses a source/digest-pinned ARM64 artifact. artifact_url is a short-lived oaiusercontent HTTPS download; receipt is operator-verified CI evidence for all five suites. URL is environment-only, never job arguments. No installation or candidate execution. Probe release_status before retry; promote separately through project_deploy.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ReleasePrepareInput) (*mcp.CallToolResult, jobs.Snapshot, error) {
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return nil, jobs.Snapshot{}, errors.New("release_prepare requires retry key")
		}
		in.IdempotencyKey = owner(ctx) + "\x00" + in.IdempotencyKey
		out, e := Start(m, store, in)
		return nil, out, e
	}))
	add(tooldef.New(&mcp.Tool{Name: "release_status", Description: "Return one compact native preparation job state and validated bounded final metadata. UNTRUSTED evidence; no deployment authorization or replay.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in ReleaseStatusInput) (*mcp.CallToolResult, ReleaseStatus, error) {
		out, e := Read(m, in.JobID)
		return nil, out, e
	}))
}
func Compat(ctx context.Context, m *jobs.Manager, store *idempotency.Store, in ExecStartInput, owner func(context.Context) string) (jobs.Snapshot, error) {
	if len(in.Args) != 2 || len(in.Args[1]) > 16384 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" {
		return jobs.Snapshot{}, errors.New("release-prepare requires JSON and no execution overrides")
	}
	if _, e := m.Policy().CheckWorkdir(in.Workdir); e != nil {
		return jobs.Snapshot{}, e
	}
	var input ReleasePrepareInput
	if e := json.Unmarshal([]byte(in.Args[1]), &input); e != nil {
		return jobs.Snapshot{}, errors.New("invalid release JSON")
	}
	if input.IdempotencyKey != "" && input.IdempotencyKey != in.IdempotencyKey {
		return jobs.Snapshot{}, errors.New("release retry key mismatch")
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return jobs.Snapshot{}, errors.New("release_prepare requires retry key")
	}
	input.IdempotencyKey = owner(ctx) + "\x00" + in.IdempotencyKey
	return Start(m, store, input)
}

type ReleaseBuildPrepareInput = integration.ReleaseBuildPrepareInput
type ExecStartInput = toolcontract.ExecStartInput
