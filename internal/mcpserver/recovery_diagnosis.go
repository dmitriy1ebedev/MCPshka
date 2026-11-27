package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/tools/recovery"
	"strings"
)

type RecoveryDiagnosisInput = recovery.RecoveryDiagnosisInput
type RecoveryDiagnosisResult = recovery.RecoveryDiagnosisResult

var classifyClientRefusal = recovery.ClassifyClientRefusal

func recoveryDiagnosis(ctx context.Context, m *jobs.Manager, s *projectlease.Service, in RecoveryDiagnosisInput) (RecoveryDiagnosisResult, error) {
	return recovery.Diagnose(ctx, recovery.Deps{Manager: m, Leases: s, Owner: uploadOwner, AuthorizeUpload: authorizeUploadReceipt}, in)
}
func registerRecoveryDiagnosis(r *toolRegistry, m *jobs.Manager, s *projectlease.Service) {
	recovery.Register(r.install, recovery.Deps{Manager: m, Leases: s, Owner: uploadOwner, AuthorizeUpload: authorizeUploadReceipt})
}

func isRecoveryCompat(name string, in map[string]any) bool {
	if name != "exec_start" {
		return false
	}
	command, _ := in["command"].(string)
	args, _ := in["args"].([]any)
	return (command == "mcpshka" || command == "/usr/local/bin/mcpshka") && len(args) > 0 && args[0] == "recovery-diagnose"
}
func recoveryDiagnosisCompat(ctx context.Context, m *jobs.Manager, s *projectlease.Service, in ExecStartInput) (jobs.Snapshot, error) {
	if len(in.Args) != 2 || len(in.Args[1]) > 4096 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return jobs.Snapshot{}, errors.New("recovery-diagnose requires bounded typed input, retry key and no overrides")
	}
	if _, e := m.Policy().CheckWorkdir(in.Workdir); e != nil {
		return jobs.Snapshot{}, e
	}
	var request RecoveryDiagnosisInput
	if e := json.Unmarshal([]byte(in.Args[1]), &request); e != nil {
		return jobs.Snapshot{}, e
	}
	out, e := recoveryDiagnosis(ctx, m, s, request)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	return m.StoreReceipt("mcpshka", []string{"recovery-diagnose", out.Root, "owner-sha256:" + yieldHash(uploadOwner(ctx)), "request-sha256:" + yieldHash(in.Args[1])}, in.Workdir, string(raw))
}
