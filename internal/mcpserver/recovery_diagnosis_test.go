package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/jobs"
	"mcpshka/internal/workflow"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func diagnosisFailedJob(t *testing.T, m *jobs.Manager, root, log string) *jobs.Manager {
	t.Helper()
	id := "failed-config"
	exit := 1
	dir := filepath.Join(m.StateDir(), id)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	for stream, data := range map[string]string{"stdout": log, "stderr": ""} {
		if e := os.WriteFile(filepath.Join(dir, stream+".log"), []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	raw, _ := json.Marshal(jobs.Snapshot{ID: id, Workdir: root, Command: "go", State: jobs.Failed, ExitCode: &exit, OutputStoredBytes: int64(len(log))})
	if e := m.Database().Put("jobs/"+id+"/metadata.json", raw); e != nil {
		t.Fatal(e)
	}
	reloaded, e := jobs.New(m.StateDir(), m.Policy())
	if e != nil {
		t.Fatal(e)
	}
	return reloaded
}
func TestRecoveryDiagnosisDIAGConfigAndNoPersistentIndex(t *testing.T) {
	for _, test := range []struct{ log, code string }{
		{"config: config /etc/diag/diag.json: lstat: permission denied", "PERMISSION_DENIED"},
		{"config: config /tmp/private-fixture.json: lstat: no such file or directory", "MISSING_CONFIG"},
	} {
		m, s, in := yieldFixture(t)
		m = diagnosisFailedJob(t, m, in.Path, test.log)
		out, e := recoveryDiagnosis(context.Background(), m, s, RecoveryDiagnosisInput{Path: in.Path, JobID: "failed-config"})
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, f := range out.Findings {
			found = found || f.Code == test.code
		}
		if !found || out.RetryAllowed || out.ExitCode == nil || *out.ExitCode != 1 {
			t.Fatalf("bad diagnosis: %+v", out)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), test.log) || strings.Contains(string(raw), "/etc/diag/diag.json") {
			t.Fatal("raw output retained in result")
		}
		if _, e := m.Database().Get("searchindex/v1/health"); !os.IsNotExist(e) {
			t.Fatalf("read-only diagnosis wrote index health: %v", e)
		}
		if _, e := s.ValidateHolder(in.Path, in.Token); e != nil {
			t.Fatal("diagnosis touched owner")
		}
	}
}
func TestRecoveryDiagnosisNativeCompatAndPrivateReportedError(t *testing.T) {
	for _, compat := range []bool{false, true} {
		m, _, in := yieldFixture(t)
		client := uploadClient(t, m, "holder", "operator")
		request := RecoveryDiagnosisInput{Path: in.Path, ReportedError: "provider quota exhausted; private-error-marker"}
		name := "interaction_recovery_diagnose"
		var args any = request
		if compat {
			name = "exec_start"
			raw, _ := json.Marshal(request)
			args = ExecStartInput{Command: "mcpshka", Args: []string{"recovery-diagnose", string(raw)}, Workdir: in.Path, IdempotencyKey: "diagnose"}
		}
		result, e := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil || result.IsError {
			t.Fatalf("diagnosis: %+v %v", result, e)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		if strings.Contains(string(raw), "private-error-marker") {
			t.Fatal("private report echoed")
		}
		if compat {
			var snap jobs.Snapshot
			json.Unmarshal(raw, &snap)
			if len(snap.Args) != 4 {
				t.Fatal("unsanitized diagnostic receipt")
			}
			foreign := uploadClient(t, m, "other", "operator")
			for _, tool := range []string{"job_status", "job_wait", "job_output"} {
				res, e := foreign.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"job_id": snap.ID}})
				if e == nil && !res.IsError {
					t.Fatal("foreign identity read private receipt")
				}
			}
			input, _ := object(args)
			probe, e := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: "exec_start", Key: "diagnose", Input: input}})
			if e != nil || probe.IsError {
				t.Fatalf("scoped receipt missing: %+v %v", probe, e)
			}
		}
	}
}
func TestRecoveryDiagnosisUnknownJobAndProjectScope(t *testing.T) {
	m, s, in := yieldFixture(t)
	out, e := recoveryDiagnosis(context.Background(), m, s, RecoveryDiagnosisInput{Path: in.Path, JobID: "missing"})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, f := range out.Findings {
		found = found || f.Code == "JOB_UNKNOWN"
	}
	if !found || out.RetryAllowed {
		t.Fatal(out)
	}
	if _, e := recoveryDiagnosis(context.Background(), m, s, RecoveryDiagnosisInput{Path: in.Path, ReportedError: strings.Repeat("x", 2049)}); e == nil {
		t.Fatal("unbounded client report accepted")
	}
}

func TestRecoveryDiagnosisMalformedWorkflowClaimWithoutJobs(t *testing.T) {
	m, s, in := yieldFixture(t)
	id := "diagnosis-held-workflow"
	raw, _ := json.Marshal(workflow.Run{ID: id, Root: in.Path, State: "running"})
	if e := m.Database().Put("pipelines/v1/runs/"+id, raw); e != nil {
		t.Fatal(e)
	}
	key := fmt.Sprintf("pipelines/v1/claims/%x", sha256.Sum256([]byte(in.Path)))
	if e := m.Database().Put(key, []byte(id)); e != nil {
		t.Fatal(e)
	}
	out, e := recoveryDiagnosis(context.Background(), m, s, RecoveryDiagnosisInput{Path: in.Path})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, f := range out.Findings {
		found = found || f.Code == "CHECKPOINT_INCOMPLETE"
	}
	if !found || out.ActiveJobs != 0 || out.WorkflowID != "" || out.RetryAllowed {
		t.Fatalf("claim lost: %+v", out)
	}
	if _, e := s.ValidateHolder(in.Path, in.Token); e != nil {
		t.Fatal("diagnosis released holder")
	}
	if _, e := workflow.New(m).ProjectClaim(in.Path); e == nil {
		t.Fatal("malformed claim admitted as executable")
	}
	if claim, e := m.Database().Get(key); e != nil || string(claim) != id {
		t.Fatal("diagnosis discarded unknown claim")
	}
	if record, e := m.Database().Get("pipelines/v1/runs/" + id); e != nil || string(record) != string(raw) {
		t.Fatal("diagnosis rewrote unknown workflow")
	}
}
