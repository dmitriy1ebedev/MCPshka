package codex

import (
	"strings"
	"testing"
)

func admissionMockTask(id string) CodexAdmissionTaskInput {
	return CodexAdmissionTaskInput{
		ID: id, SessionID: "session_" + id + "testing", Worktree: "/tmp/r3-" + id,
		RoadmapSHA: strings.Repeat("a", 64), Message: "isolated review " + id, Model: "mock-model",
		ReasoningEffort: "high", Revision: 1, CatalogVerified: true, OwnerVerified: true,
		SessionIdle: true, WriterLeaseFree: true, ControlPathHealthy: true, QuotaVerified: true,
		QuotaAvailable: true, WallSeconds: 120, CPUPercent: 50, MemoryMiB: 128, OutputKiB: 32,
	}
}

func TestCodexAdmissionPreviewNeverDispatches(t *testing.T) {
	a, b, c := admissionMockTask("alpha"), admissionMockTask("beta"), admissionMockTask("gamma")
	a.SessionIdle = false
	c.QuotaVerified = false
	in := CodexAdmissionPreviewInput{Tasks: []CodexAdmissionTaskInput{c, b, a},
		Models: []CodexAdmissionModelInput{{ID: "mock-model", Efforts: []string{"low", "high"}}}, MaxConcurrent: 3}
	out, err := AdmissionPreview(in)
	if err != nil || out.DispatchEnabled || out.Trust != "UNTRUSTED" || len(out.Decisions) != 3 {
		t.Fatal(out, err)
	}
	if out.Decisions[0].Status != "HOLD" || out.Decisions[1].Status != "READY_FOR_APPROVAL" || out.Decisions[2].Status != "HOLD" {
		t.Fatal(out)
	}
	for _, v := range out.Decisions {
		if v.DispatchEnabled {
			t.Fatal("an offline preview launched Codex")
		}
	}
	in.Tasks[1].ReasoningEffort = "xhigh"
	out, err = AdmissionPreview(in)
	if err != nil || out.Decisions[1].Status != "HOLD" {
		t.Fatal(out, err)
	}
}
