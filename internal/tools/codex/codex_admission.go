package codex

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/codexcoord"
	"mcpshka/internal/tooldef"
)

// All inputs are untrusted caller assertions. This read-only endpoint is for
// offline planning and cannot establish real ownership or model availability.
type CodexAdmissionTaskInput struct {
	ID                 string `json:"id"`
	SessionID          string `json:"session_id"`
	Worktree           string `json:"worktree"`
	RoadmapSHA         string `json:"roadmap_sha256"`
	Message            string `json:"message"`
	Model              string `json:"model"`
	ReasoningEffort    string `json:"reasoning_effort,omitempty"`
	Revision           uint64 `json:"revision"`
	CatalogVerified    bool   `json:"catalog_verified"`
	OwnerVerified      bool   `json:"owner_verified"`
	SessionIdle        bool   `json:"session_idle"`
	WriterLeaseFree    bool   `json:"writer_lease_free"`
	ControlPathHealthy bool   `json:"control_path_healthy"`
	QuotaVerified      bool   `json:"quota_verified"`
	QuotaAvailable     bool   `json:"quota_available"`
	WallSeconds        int    `json:"wall_seconds"`
	CPUPercent         int    `json:"cpu_percent"`
	MemoryMiB          int    `json:"memory_mib"`
	OutputKiB          int    `json:"output_kib"`
}

type CodexAdmissionModelInput struct {
	ID      string   `json:"id"`
	Efforts []string `json:"efforts"`
}
type CodexAdmissionPreviewInput struct {
	Tasks         []CodexAdmissionTaskInput  `json:"tasks"`
	Models        []CodexAdmissionModelInput `json:"models"`
	MaxConcurrent int                        `json:"max_concurrent"`
}
type CodexAdmissionPreviewOutput struct {
	Trust           string                         `json:"trust"`
	Source          string                         `json:"source"`
	DispatchEnabled bool                           `json:"dispatch_enabled"`
	Decisions       []codexcoord.AdmissionDecision `json:"decisions"`
}

func AdmissionPreview(in CodexAdmissionPreviewInput) (CodexAdmissionPreviewOutput, error) {
	out := CodexAdmissionPreviewOutput{Trust: "UNTRUSTED", Source: "caller_supplied_offline_simulation", DispatchEnabled: false}
	if len(in.Tasks) == 0 || len(in.Tasks) > 32 || len(in.Models) == 0 || len(in.Models) > 32 {
		return out, errors.New("admission preview budget exceeded")
	}
	catalog := make([]codexcoord.Model, 0, len(in.Models))
	for _, m := range in.Models {
		if len(m.Efforts) > 5 {
			return out, errors.New("model reasoning effort budget exceeded")
		}
		catalog = append(catalog, codexcoord.Model{ID: m.ID, Efforts: m.Efforts})
	}
	requests := make([]codexcoord.AdmissionRequest, 0, len(in.Tasks))
	for _, v := range in.Tasks {
		t := codexcoord.Task{ID: v.ID, Provider: "codex", SessionID: v.SessionID,
			Worktree: v.Worktree, RoadmapSHA: v.RoadmapSHA, Message: v.Message, Model: v.Model,
			Effort: v.ReasoningEffort, State: codexcoord.Queued, Revision: v.Revision}
		facts := codexcoord.ScopeFacts{SessionID: v.SessionID, Worktree: v.Worktree, RoadmapSHA: v.RoadmapSHA,
			CatalogVerified: v.CatalogVerified, OwnerVerified: v.OwnerVerified, SessionIdle: v.SessionIdle,
			WriterLeaseFree: v.WriterLeaseFree, ControlPathHealthy: v.ControlPathHealthy,
			QuotaVerified: v.QuotaVerified, QuotaAvailable: v.QuotaAvailable}
		b := codexcoord.ResourceBudget{WallSeconds: v.WallSeconds, CPUPercent: v.CPUPercent,
			MemoryMiB: v.MemoryMiB, OutputKiB: v.OutputKiB}
		requests = append(requests, codexcoord.AdmissionRequest{Task: t, Facts: facts, Budget: b})
	}
	rows, err := codexcoord.AssessBatch(requests, catalog, in.MaxConcurrent)
	if err != nil {
		return out, err
	}
	out.Decisions = rows
	return out, nil
}

func RegisterAdmission(add func(tooldef.Def)) {
	closed := false
	add(tooldef.New(&mcp.Tool{
		Name:        "codex_admission_preview",
		Description: "Offline read-only bounded model/reasoning and session/worktree admission simulation. All observations are caller-supplied UNTRUSTED data; READY_FOR_APPROVAL never authorizes or launches Codex, modifies leases or changes Git.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in CodexAdmissionPreviewInput) (*mcp.CallToolResult, CodexAdmissionPreviewOutput, error) {
		out, err := AdmissionPreview(in)
		return nil, out, err
	}))
}
