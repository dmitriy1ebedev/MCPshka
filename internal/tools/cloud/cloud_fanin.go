package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/tooldef"
)

// Cloud observations are untrusted data, never release or deploy authority.
const PlanSchema = "mcpshka.cloud-plan/v1"
const ObservationSchema = "mcpshka.cloud-observation/v1"
const cloudFanInSchema = "mcpshka.cloud-fanin/v1"

var cloudDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var cloudIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type CloudPlan struct {
	Schema       string              `json:"schema"`
	ReleaseID    string              `json:"release_id"`
	SourceDigest string              `json:"source_digest"`
	Required     map[string][]string `json:"required"`
}
type CloudObservation struct {
	Schema           string `json:"schema"`
	ReleaseID        string `json:"release_id"`
	SourceDigest     string `json:"source_digest"`
	Provider         string `json:"provider"`
	Suite            string `json:"suite"`
	RunID            string `json:"run_id"`
	Status           string `json:"status"`
	ArtifactSHA256   string `json:"artifact_sha256,omitempty"`
	DeployAuthorized bool   `json:"deploy_authorized,omitempty"`
}
type CloudFanInInput struct {
	Plan         CloudPlan          `json:"plan"`
	Observations []CloudObservation `json:"observations"`
}
type CloudRun struct {
	Provider       string `json:"provider"`
	Suite          string `json:"suite"`
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
}
type CloudFanInResult struct {
	Schema           string     `json:"schema"`
	ReleaseID        string     `json:"release_id"`
	SourceDigest     string     `json:"source_digest"`
	Trust            string     `json:"trust"`
	DeployAuthorized bool       `json:"deploy_authorized"`
	State            string     `json:"state"`
	Required         int        `json:"required"`
	Observed         int        `json:"observed"`
	Missing          []string   `json:"missing"`
	Failed           []string   `json:"failed"`
	Unknown          []string   `json:"unknown"`
	Runs             []CloudRun `json:"runs"`
}

func FanIn(input CloudFanInInput) (CloudFanInResult, error) {
	p := input.Plan
	if p.Schema != PlanSchema || !cloudIDRE.MatchString(p.ReleaseID) || !cloudDigestRE.MatchString(p.SourceDigest) {
		return CloudFanInResult{}, errors.New("invalid pinned cloud plan")
	}
	if len(p.Required) == 0 || len(p.Required) > 3 || len(input.Observations) > 64 {
		return CloudFanInResult{}, errors.New("cloud plan or observations exceed budget")
	}
	allowedProviders := map[string]bool{"github": true, "gitverse": true, "moshub": true}
	expected := map[string]bool{}
	for provider, suites := range p.Required {
		if !allowedProviders[provider] || len(suites) == 0 || len(suites) > 32 {
			return CloudFanInResult{}, errors.New("invalid cloud provider suites")
		}
		for _, suite := range suites {
			if !cloudIDRE.MatchString(suite) {
				return CloudFanInResult{}, errors.New("invalid suite")
			}
			key := provider + "/" + suite
			if expected[key] {
				return CloudFanInResult{}, errors.New("duplicate suite")
			}
			expected[key] = true
		}
	}
	actual := map[string]CloudRun{}
	out := CloudFanInResult{
		Schema: cloudFanInSchema, ReleaseID: p.ReleaseID, SourceDigest: p.SourceDigest,
		Trust: "UNTRUSTED", DeployAuthorized: false,
		Missing: []string{}, Failed: []string{}, Unknown: []string{}, Runs: []CloudRun{},
	}
	for _, obs := range input.Observations {
		if obs.Schema != ObservationSchema || !cloudIDRE.MatchString(obs.Provider) || !cloudIDRE.MatchString(obs.Suite) ||
			!cloudIDRE.MatchString(obs.RunID) || obs.ReleaseID != p.ReleaseID ||
			obs.SourceDigest != p.SourceDigest || obs.DeployAuthorized ||
			(obs.ArtifactSHA256 != "" && !cloudDigestRE.MatchString(obs.ArtifactSHA256)) {
			return CloudFanInResult{}, errors.New("invalid or mismatched cloud observation")
		}
		switch obs.Status {
		case "success", "failure", "cancelled", "unknown":
		default:
			return CloudFanInResult{}, errors.New("invalid cloud observation status")
		}
		key := obs.Provider + "/" + obs.Suite
		if !expected[key] || actual[key].RunID != "" {
			return CloudFanInResult{}, errors.New("unexpected or duplicate cloud observation")
		}
		actual[key] = CloudRun{Provider: obs.Provider, Suite: obs.Suite, RunID: obs.RunID, Status: obs.Status,
			ArtifactSHA256: obs.ArtifactSHA256}
	}
	planned := make([]string, 0, len(expected))
	for key := range expected {
		planned = append(planned, key)
	}
	sort.Strings(planned)
	for _, key := range planned {
		item, ok := actual[key]
		if !ok {
			out.Missing = append(out.Missing, key)
			continue
		}
		switch item.Status {
		case "failure", "cancelled":
			out.Failed = append(out.Failed, key)
		case "unknown":
			out.Unknown = append(out.Unknown, key)
		}
		out.Runs = append(out.Runs, item)
	}
	out.Required, out.Observed = len(expected), len(actual)
	switch {
	case len(out.Failed) > 0:
		out.State = "failed"
	case len(out.Missing) > 0 || len(out.Unknown) > 0:
		out.State = "hold"
	default:
		out.State = "observed_pass"
	}
	if raw, err := json.Marshal(out); err != nil || len(raw) > 32768 {
		return CloudFanInResult{}, errors.New("cloud observation result exceeds 32 KiB")
	}
	return out, nil
}

func RegisterFanIn(add func(tooldef.Def)) {
	add(tooldef.New(&mcp.Tool{
		Name:        "cloud_fanin",
		Description: "Read-only bounded fan-in of UNTRUSTED GitHub/GitVerse/Mos.Hub observation data for a pinned source digest. Even observed_pass NEVER authorizes build promotion or deployment. No provider claims are authenticated by this tool.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CloudFanInInput) (*mcp.CallToolResult, CloudFanInResult, error) {
		result, err := FanIn(in)
		return nil, result, err
	}))
}
