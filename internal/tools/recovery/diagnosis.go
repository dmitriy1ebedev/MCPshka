package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/deploy"
	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/knowledge"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/recoverydiagnosis"
	"mcpshka/internal/searchindex"
	"mcpshka/internal/tooldef"
	"mcpshka/internal/tools/workyield"
	"mcpshka/internal/workflow"
	"path/filepath"
	"regexp"
	"time"
)

type RecoveryDiagnosisInput struct {
	Path          string `json:"path"`
	JobID         string `json:"job_id,omitempty"`
	ReportedError string `json:"reported_error,omitempty"`
}
type RecoveryDiagnosisResult struct {
	WorkflowID         string                      `json:"workflow_id,omitempty"`
	WorkflowState      string                      `json:"workflow_state,omitempty"`
	Trust              string                      `json:"trust"`
	CapturedAt         time.Time                   `json:"captured_at"`
	Root               string                      `json:"root"`
	Revision           string                      `json:"revision,omitempty"`
	JobID              string                      `json:"job_id,omitempty"`
	JobState           jobs.State                  `json:"job_state,omitempty"`
	ExitCode           *int                        `json:"exit_code,omitempty"`
	OutputSHA256       string                      `json:"output_sha256,omitempty"`
	ClientReportSHA256 string                      `json:"client_report_sha256,omitempty"`
	ActiveJobs         int                         `json:"active_jobs"`
	UnknownJobs        int                         `json:"unknown_jobs"`
	Findings           []recoverydiagnosis.Finding `json:"findings"`
	RetryAllowed       bool                        `json:"retry_allowed"`
	EvidenceTruncated  bool                        `json:"evidence_truncated"`
	SimilarCases       []string                    `json:"similar_case_ids,omitempty"`
	ExperienceState    string                      `json:"experience_state"`
	Warnings           []string                    `json:"warnings,omitempty"`
	// MCP-047: a client-reported refusal is classified, and it is never
	// attributed to MCPshka without a server receipt (job/receipt id).
	ClientRefusalClass   string `json:"client_refusal_class,omitempty"`
	ClientNextSafeAction string `json:"client_next_safe_action,omitempty"`
	ServerReceipt        string `json:"server_receipt,omitempty"`
	// MCP-039: typed code and whether the call is proven to have reached MCPshka.
	ClientRefusalCode string `json:"client_refusal_code,omitempty"`
	DispatchedToMCP   string `json:"dispatched_to_mcp,omitempty"`
}

var (
	refusalSafety    = regexp.MustCompile(`(?i)safety|unsafe|not allowed to (?:call|run|use)|declin|refus|can't help|cannot help|blocked by (?:the )?(?:assistant|model)`)
	refusalSchema    = regexp.MustCompile(`(?i)schema|unexpected (?:additional )?propert|validat|invalid (?:argument|param)|unknown tool|tool not found|CLIENT_SCHEMA_STALE`)
	refusalAuth      = regexp.MustCompile(`(?i)unauthori[sz]ed|\b401\b|\b403\b|forbidden|oauth|token expired|re-?authenticat|login|publickey|host key verification`)
	refusalPolicy    = regexp.MustCompile(`(?i)POLICY_DENIED|OWNER_BUSY|native_only|agent_consent|face id|admin_session`)
	refusalWeakGate  = regexp.MustCompile(`(?i)\bapproval|\bHOLD\b|\blease\b`)
	refusalTransport = regexp.MustCompile(`(?i)timeout|timed out|connection|network|\b50[234]\b|EOF|reset by peer|unreachable|disconnected`)
	receiptID        = regexp.MustCompile(`\b(?:job|receipt|idempotency)[ _-]?(?:id)?[":= ]+[0-9a-z]{6,}`)
)

// classifyClientRefusal returns the refusal class, its next safe action and
// whether the text names a server receipt. Order matters: a server policy
// verdict wins over a generic word like "refused".
func ClassifyClientRefusal(text string) (class, next, receipt string) {
	receipt = "none_reported: do not attribute the refusal to MCPshka; the call may never have reached the server"
	if receiptID.MatchString(text) {
		receipt = "reported: reconcile it with job_status/mutation_probe before any retry"
	}
	switch {
	case refusalPolicy.MatchString(text):
		return "policy", "follow the server next_action (approval/lease/Face ID); do not retry with another tool to bypass it", receipt
	case refusalSchema.MatchString(text):
		return "schema_mismatch", "tool_resolve for the exact schema, then tool_invoke(name, arguments)", receipt
	case refusalAuth.MatchString(text):
		return "auth", "refresh the client connector/OAuth login; nothing on the server changed", receipt
	case refusalSafety.MatchString(text):
		return "tool_safety", "client-reported safety refusal: inspect read-only state and original receipts; use only independently authorized least-privilege operations, never disguise or replay the blocked call", receipt
	case refusalWeakGate.MatchString(text):
		return "policy", "follow the server next_action (approval/lease/Face ID); do not retry with another tool to bypass it", receipt
	case refusalTransport.MatchString(text):
		return "transport", "treat the effect as UNKNOWN: probe with the same idempotency_key before retrying", receipt
	}
	return "unknown", "continue with read-only diagnosis; record the exact client text and time", receipt
}

type Deps struct {
	Manager         *jobs.Manager
	Leases          *projectlease.Service
	Owner           func(context.Context) string
	AuthorizeUpload func(context.Context, *jobs.Manager, string) error
}

func Diagnose(ctx context.Context, d Deps, in RecoveryDiagnosisInput) (RecoveryDiagnosisResult, error) {
	m, s := d.Manager, d.Leases
	var out RecoveryDiagnosisResult
	if len(in.JobID) > 128 || len(in.ReportedError) > 2048 {
		return out, errors.New("diagnosis input exceeds budget")
	}
	if e := ctx.Err(); e != nil {
		return out, e
	}
	git := gitops.New(m.Policy())
	root, e := git.RepoRoot(in.Path)
	if e != nil {
		return out, e
	}
	out = RecoveryDiagnosisResult{Trust: "UNTRUSTED", CapturedAt: time.Now().UTC(), Root: root, JobID: in.JobID, ExperienceState: "not_requested"}
	facts := recoverydiagnosis.Evidence{JobKnown: true, JobState: "not_requested"}
	_, facts.Authenticated = authn.IdentityFromContext(ctx)
	out.Revision, e = git.Revision(root)
	if e != nil {
		facts.CheckpointIncomplete = true
		out.Warnings = append(out.Warnings, "git_revision_unverified")
	}
	lease, e := s.Status(root)
	if e != nil {
		facts.CheckpointIncomplete = true
		out.Warnings = append(out.Warnings, "lease_authority_unverified")
	} else {
		facts.LeaseHeld = lease.Held
		facts.OwnerVerified = lease.OwnerLiveness == "verified"
		facts.LegacyOwnerUnbound = lease.Held && lease.ExecutionID == "" && lease.OwnerStartTicks == 0
		facts.StaleHeartbeat = lease.ReconciliationRequired
	}
	activity := m.ProjectActivity(root, 16)
	out.ActiveJobs = activity.Count
	out.UnknownJobs = activity.UnknownCount
	out.EvidenceTruncated = activity.Truncated
	if activity.UnknownCount > 0 {
		facts.JobKnown = false
	}
	reconciled, e := deploy.New(m.Policy(), filepath.Join(filepath.Dir(m.StateDir()), "deploys")).Reconcile()
	facts.DeploymentUnverified = reconciled.Pending || e != nil
	for _, d := range reconciled.Deployments {
		facts.HistoricalDeployUnknown = facts.HistoricalDeployUnknown || d.ReasonCode == "PERMANENTLY_RETIRED_OUTCOME_UNKNOWN"
	}
	if claim, e := workflow.New(m).ProjectClaim(root); e != nil {
		facts.CheckpointIncomplete = true
		out.Warnings = append(out.Warnings, "workflow_authority_unverified")
	} else if claim != nil {
		facts.WorkflowClaim = true
		out.WorkflowID, out.WorkflowState = claim.ID, claim.State
	}
	if in.JobID != "" {
		if e := workyield.AuthorizeReceipt(ctx, m, in.JobID, d.Owner); e != nil {
			if _, known := m.Get(in.JobID); known == nil {
				return out, e
			}
		}
		if e := d.AuthorizeUpload(ctx, m, in.JobID); e != nil {
			if _, known := m.Get(in.JobID); known == nil {
				return out, e
			}
		}
		job, e := m.Get(in.JobID)
		if e != nil {
			facts.JobKnown = false
			out.JobState = jobs.Unknown
		} else {
			jobRoot, e := git.RepoRoot(job.Workdir)
			if e != nil || jobRoot != root {
				return out, errors.New("job does not belong to this verified project")
			}
			facts.JobKnown = true
			facts.JobState = string(job.State)
			facts.ResourceLimited = job.ResourceViolation != ""
			out.JobState = job.State
			out.ExitCode = job.ExitCode
			if job.State == jobs.Failed {
				logs := ""
				for _, stream := range []string{"stdout", "stderr"} {
					part, e := m.Read(in.JobID, stream, 0, 4096)
					if e != nil {
						out.Warnings = append(out.Warnings, "job_output_unavailable")
						continue
					}
					logs += part.Text
					if part.Truncated || len(part.Text) >= 4096 {
						out.EvidenceTruncated = true
					}
				}
				facts.Log = logs
				out.OutputSHA256 = workyield.Hash(logs)
				out.EvidenceTruncated = out.EvidenceTruncated || job.OutputDroppedBytes > 0
			}
		}
	}
	diagnosis := recoverydiagnosis.Diagnose(facts)
	out.Findings = diagnosis.Findings
	out.EvidenceTruncated = out.EvidenceTruncated || diagnosis.EvidenceTruncated
	if activity.UnknownCount > 0 {
		present := false
		for _, f := range out.Findings {
			present = present || f.Code == "JOB_UNKNOWN"
		}
		if !present {
			extra := recoverydiagnosis.Diagnose(recoverydiagnosis.Evidence{JobKnown: false})
			out.Findings = append(out.Findings, extra.Findings...)
		}
	}
	if in.ReportedError != "" {
		out.ClientReportSHA256 = workyield.Hash(in.ReportedError)
		out.ClientRefusalClass, out.ClientNextSafeAction, out.ServerReceipt = ClassifyClientRefusal(in.ReportedError)
		out.ClientRefusalCode, out.DispatchedToMCP = ClientRefusalCode(out.ClientRefusalClass, in.ReportedError, out.ServerReceipt)
		report := recoverydiagnosis.Diagnose(recoverydiagnosis.Evidence{JobKnown: true, JobState: "interaction_failed", Log: in.ReportedError})
		for _, f := range report.Findings {
			seen := false
			for _, prior := range out.Findings {
				seen = seen || prior.Code == f.Code
			}
			if !seen {
				out.Findings = append(out.Findings, f)
			}
		}
	}
	if len(out.Findings) > 0 {
		store, e := knowledge.FromDatabase(m.Database())
		index := &searchindex.Index{}
		if e == nil {
			e = index.Reindex(ctx, &searchindex.KnowledgeSource{Store: store}, 1000, searchindex.DefaultMaxBytes)
		}
		var hits []searchindex.Hit
		if e == nil {
			hits, e = index.Search(ctx, searchindex.Query{Text: out.Findings[0].Code, Limit: 2, MaxBytes: 4096})
		}
		out.ExperienceState = "searched_visible_case_evidence"
		if e != nil {
			out.ExperienceState = "lookup_unavailable"
		} else {
			for _, h := range hits {
				out.SimilarCases = append(out.SimilarCases, h.Record.ID)
			}
		}
	}
	raw, e := json.Marshal(out)
	if e != nil || len(raw) > 16384 {
		return RecoveryDiagnosisResult{}, errors.New("diagnosis output exceeds budget")
	}
	return out, nil
}
func Register(add func(tooldef.Def), d Deps) {
	add(tooldef.New(&mcp.Tool{Name: "interaction_recovery_diagnose", Description: "Bounded read-only classification of live project/lease/jobs/deployment and failed-job output, including DIAG missing-config vs permission regression. Client-reported errors and logs remain UNTRUSTED clues. Consults visible Knowledge Core case IDs. Returns typed recovery guidance, never releases owners, grants approval, launches a job or permits unknown replay.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in RecoveryDiagnosisInput) (*mcp.CallToolResult, RecoveryDiagnosisResult, error) {
		out, e := Diagnose(ctx, d, in)
		return nil, out, e
	}))
}
