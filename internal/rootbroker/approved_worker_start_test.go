package rootbroker

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMissingWritePathIsRefusedBeforeSandbox(t *testing.T) {
	dir := t.TempDir()
	p := ApprovedPlan{Write: []string{dir}}
	if m := missingWritePath(p); m != "" {
		t.Fatal("existing path refused:", m)
	}
	p.Write = append(p.Write, filepath.Join(dir, "lab"))
	if m := missingWritePath(p); !strings.Contains(m, filepath.Join(dir, "lab")) || !strings.Contains(m, dir) {
		t.Fatalf("missing path not named with its parent: %q", m)
	}
}

func TestVanishedWorkerWithoutStartMarkRanNothing(t *testing.T) {
	id := strings.Repeat("e", 32)
	now := time.Now().UTC()
	r := endedWithoutResult(ApprovedResult{ID: id, State: "RUNNING", WorkerProtocol: 1}, now)
	if r.State != "FAILED" || r.Effect != "none" || !strings.Contains(r.Error, "never started") {
		t.Fatalf("pre-exec failure: %+v", r)
	}
	if annotateApproved(r, now).Effect != "none" {
		t.Fatal("annotation lost proven effect none")
	}
	r = endedWithoutResult(ApprovedResult{ID: id, State: "RUNNING", WorkerProtocol: 1, WorkerStartedAt: &now}, now)
	if annotateApproved(r, now).Effect != "unknown" {
		t.Fatalf("started worker must stay unknown: %+v", r)
	}
	// Legacy records carry no mark: absence proves nothing.
	if annotateApproved(endedWithoutResult(ApprovedResult{ID: id, State: "RUNNING"}, now), now).Effect != "unknown" {
		t.Fatal("legacy record guessed effect none")
	}
}

// MCP-188: the broker runs with ProtectHome=tmpfs, so write paths under
// /root are checked by the inspector (which sees /root), not the broker.
func TestInspectorReportsMissingWritePath(t *testing.T) {
	dir := t.TempDir()
	p := ApprovedPlan{Executable: "/bin/true", Workdir: dir, Write: []string{dir}}
	got := inspectApproved(p)
	if got.Error != "" {
		t.Skip(got.Error) // /bin/true not root-owned on this host
	}
	if got.MissingWrite != "" {
		t.Fatal("existing write path reported missing:", got.MissingWrite)
	}
	p.Write = append(p.Write, filepath.Join(dir, "absent"))
	if got = inspectApproved(p); !strings.Contains(got.MissingWrite, filepath.Join(dir, "absent")) {
		t.Fatalf("missing write path not reported by the inspector: %+v", got)
	}
}
