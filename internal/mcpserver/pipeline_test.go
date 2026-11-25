package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/laneprovider"
	"mcpshka/internal/networkops"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/statestore"
)

func TestNetworkStageStatusKeepsPartialEvidenceWhenLANEUnavailable(t *testing.T) {
	control := controlplane.Snapshot{Primary: "openai_tunnel", EmergencyState: "healthy"}
	lane := laneprovider.Status{}
	network := networkops.StateDigest{}

	partial := makeNetworkStageStatus(control, lane, errors.New("permission denied"), network, nil)
	if !partial.Partial || partial.Control.Primary != control.Primary ||
		partial.LANE != nil || partial.LANEState != "unknown" ||
		partial.Network == nil || partial.NetworkState != "observed" || len(partial.Warnings) != 1 {
		t.Fatalf("LANE failure erased good control/network evidence: %+v", partial)
	}
	encoded, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["lane"]; present {
		t.Fatalf("unknown LANE status serialized as verified zero value: %s", encoded)
	}
	if _, present := fields["network"]; !present {
		t.Fatalf("valid independent network observation omitted: %s", encoded)
	}

	unavailable := makeNetworkStageStatus(control, lane, errors.New("denied"), network, errors.New("network unavailable"))
	if !unavailable.Partial || unavailable.LANE != nil || unavailable.Network != nil ||
		unavailable.LANEState != "unknown" || unavailable.NetworkState != "unknown" || len(unavailable.Warnings) != 2 {
		t.Fatalf("unknown observations incorrectly promoted: %+v", unavailable)
	}
	complete := makeNetworkStageStatus(control, lane, nil, network, nil)
	if complete.Partial || complete.LANE == nil || complete.Network == nil ||
		complete.LANEState != "observed" || complete.NetworkState != "observed" || len(complete.Warnings) != 0 {
		t.Fatalf("healthy status no longer complete: %+v", complete)
	}
}

func TestRescueStatusDistinguishesMissingAndUntrustedHelper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rescue.py")
	status, missing, err := rescueUnavailable(path)
	if err != nil || !missing || status["state"] != "unconfigured" ||
		status["helper_installed"] != false || status["recovery_ready"] != false ||
		status["verified_independent_oob"] != 0 {
		t.Fatalf("missing helper falsely declared healthy: %+v %t %v", status, missing, err)
	}
	if err := os.WriteFile(path, []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	status, missing, err = rescueUnavailable(path)
	if err != nil || missing || status != nil {
		t.Fatalf("existing helper bypassed broker verification: %+v %t %v", status, missing, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rescueUnavailable(path); err == nil {
		t.Fatal("untrusted symlink helper path accepted")
	}
}

func TestInteractiveLeaseAllowsOnlyItsOwnerToPatch(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "note.txt")
	if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := newWorkflowCoordinator(manager)
	files := filesystem.New(p)
	server := newServerWithCoordinator(manager, files, nil, coordinator)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "lease-test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	holder, err := coordinator.leases.AcquireTracked(root, "operator", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	read, err := files.Read(target, 0, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	patch := map[string]any{"path": target, "old_text": "before", "new_text": "after", "expected_sha256": read.SHA256, "idempotency_key": "patch-with-holder"}
	check := func(args map[string]any, wantError bool) {
		t.Helper()
		result, callErr := cs.CallTool(ctx, &mcp.CallToolParams{Name: "fs_patch", Arguments: args})
		if (callErr != nil || result.IsError) != wantError {
			t.Fatalf("patch status error=%v receipt=%+v wantError=%v", callErr, result, wantError)
		}
	}
	wrong := make(map[string]any, len(patch)+1)
	for k, v := range patch {
		wrong[k] = v
	}
	wrong["lease_token"], wrong["idempotency_key"] = "invalid", "patch-with-wrong-holder"
	check(wrong, true)
	none := make(map[string]any, len(patch))
	for k, v := range patch {
		none[k] = v
	}
	none["idempotency_key"] = "patch-tokenless"
	check(none, true)
	patch["lease_token"] = holder.Token
	check(patch, false)
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "after" {
		t.Fatalf("patch result: %q %v", content, err)
	}
	if _, err := coordinator.leases.Release(root, holder.Token); err != nil {
		t.Fatal(err)
	}
	read, err = files.Read(target, 0, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	patch["old_text"], patch["new_text"], patch["expected_sha256"], patch["idempotency_key"] = "after", "stale", read.SHA256, "patch-stale-token"
	check(patch, true)
}

func TestPipelineMCPContractsAndWriterFences(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	for name, data := range map[string]string{"go.mod": "module pipeline.test\n\ngo 1.26\n", "ROADMAP.md": "## P7 — Canary\n- [ ] execution\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ".mcpshka"), 0700); err != nil {
		t.Fatal(err)
	}
	stages := []string{"inspect", "modify", "test", "review", "commit", "build", "deploy", "reconnect", "verify", "cleanup"}
	profile := projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "canary", Match: projectprofile.Match{Module: "pipeline.test"}, Workflows: map[string]projectprofile.Workflow{}, Pipelines: map[string]projectprofile.Pipeline{"release": {Stages: stages, Rollback: "rollback", Finalizer: "finalizer"}}}
	for _, name := range append(stages, "rollback", "finalizer") {
		profile.Workflows[name] = projectprofile.Workflow{Command: "python3", Args: []string{"-c", "pass"}, TimeoutSeconds: 5, OutputLimitBytes: 1024}
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".mcpshka/profile.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Canary", "-c", "user.email=canary@localhost", "commit", "-qm", "seed"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("seed: %s %v", out, err)
		}
	}
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := newWorkflowCoordinator(manager)
	admission := coordinator.pipelines.Admission
	coordinator.pipelines.Admission = func(string) error { return errors.New("controller admission closed") }
	server := newServerWithCoordinator(manager, filesystem.New(p), nil, coordinator)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "canary", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	scan, err := roadmapscan.Load(filepath.Join(root, "ROADMAP.md"), "")
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"id": "mcp-canary", "path": root, "pipeline": "release", "roadmap_sha256": scan.SHA256, "scope": "P7", "idempotency_key": "start-canary"}
	deniedArgs := map[string]any{"id": "denied-canary", "path": root, "pipeline": "release", "roadmap_sha256": scan.SHA256, "scope": "P7", "idempotency_key": "denied-start"}
	resDenied, denyErr := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workflow_start", Arguments: deniedArgs})
	if denyErr == nil && !resDenied.IsError {
		t.Fatal("API discarded configured controller admission")
	}
	coordinator.pipelines.Admission = admission
	for i := 0; i < 2; i++ {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workflow_start", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("start/retry: %+v %v", res, err)
		}
	}
	contextResult, err := projectContext(manager, ProjectContextInput{Path: root, ExpectedRoadmapSHA: scan.SHA256})
	if err != nil || contextResult.Claim == nil || contextResult.Claim.ID != "mcp-canary" || contextResult.CurrentItem == nil || contextResult.CurrentItem.ID != "P7" {
		t.Fatalf("handoff lost durable claim: %+v %v", contextResult, err)
	}
	resContext, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "project_context", Arguments: map[string]any{"path": root, "expected_roadmap_sha256": scan.SHA256}})
	if err != nil || resContext.IsError {
		t.Fatalf("context tool: %+v %v", resContext, err)
	}
	probes := []struct {
		name string
		args map[string]any
	}{
		{"project_lease_acquire", map[string]any{"path": root, "owner": "other", "idempotency_key": "other-lease"}},
		{"exec_start", map[string]any{"command": "python3", "args": []string{"-c", "pass"}, "workdir": root, "idempotency_key": "other-job"}},
		{"fs_patch", map[string]any{"path": filepath.Join(root, "ROADMAP.md"), "old_text": "execution", "new_text": "changed", "expected_sha256": scan.SHA256, "idempotency_key": "other-patch"}},
	}
	for _, probe := range probes {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: probe.name, Arguments: probe.args})
		if err == nil && !res.IsError {
			t.Fatalf("writer bypass: %s", probe.name)
		}
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "workflow_status", Arguments: map[string]any{"id": "mcp-canary"}})
	if err != nil || res.IsError {
		t.Fatalf("status: %+v %v", res, err)
	}
}
