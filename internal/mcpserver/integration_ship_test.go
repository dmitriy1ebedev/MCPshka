package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
)

// fakeConveyor plays the integration tools: every call is recorded, and the
// sealed test and the worker advance only when the test moves them on.
type fakeConveyor struct {
	t         *testing.T
	calls     []string
	keys      map[string]int
	enqueued  bool
	prepared  int // polls until SEALED
	exec      string
	launched  bool
	result    string // "" running, else SUCCEEDED/FAILED
	fenced    bool
	approved  bool
	worker    bool
	testState string
	unitAlive bool
	recorded  bool // worker stored its effect and waits for fence
	released  bool
	published bool
}

func (f *fakeConveyor) registry() *toolRegistry {
	r := &toolRegistry{invokers: map[string]toolInvoker{}}
	add := func(name string, fn func(map[string]any) any) {
		r.invokers[name] = func(_ context.Context, args map[string]any) (*mcp.CallToolResult, any, error) {
			f.calls = append(f.calls, name)
			if k, ok := args["idempotency_key"].(string); ok {
				f.keys[name+"|"+k]++
			}
			return nil, fn(args), nil
		}
	}
	add("integration_status", func(map[string]any) any {
		if !f.enqueued {
			return map[string]any{"found": false}
		}
		state := "pending"
		if f.published {
			state = "published"
		}
		return map[string]any{"found": true, "item": map[string]any{"state": state, "request": map[string]any{"commit": shipTestCommit}}}
	})
	add("integration_enqueue", func(args map[string]any) any {
		if args["target_head"] != strings.Repeat("e", 40) || args["roadmap_sha256"] != strings.Repeat("d", 64) {
			f.t.Fatalf("enqueue pinned to the wrong target: %v", args)
		}
		f.enqueued = true
		return map[string]any{}
	})
	add("integration_test_prepare", func(map[string]any) any {
		f.prepared++
		state := "PREPARING"
		if f.prepared > 1 {
			state = "SEALED"
		}
		return map[string]any{"state": state, "permit_id": strings.Repeat("1", 32), "launch_id": strings.Repeat("2", 32)}
	})
	add("exec_start", func(args map[string]any) any {
		action := args["args"].([]any)[1].(string)
		switch action {
		case "launch":
			f.launched = true
		case "canary-fence":
			if f.result == "" {
				f.t.Fatal("fence before a recorded result")
			}
			f.fenced = true
		}
		st := map[string]any{"state": "ACTIVE", "launches": []any{}}
		if f.launched {
			st["launches"] = []any{map[string]any{"id": strings.Repeat("2", 32), "state": "REGISTERED"}}
		}
		if f.result != "" {
			st["result"] = map[string]any{"state": "EXITED_UNFENCED"}
		}
		if f.fenced {
			st["state"], st["result"] = "FENCED", map[string]any{"state": f.result}
		}
		raw, _ := json.Marshal(st)
		f.exec = string(raw)
		return map[string]any{"id": "job-1"}
	})
	add("job_output", func(map[string]any) any { return map[string]any{"text": f.exec} })
	add("integration_approve", func(map[string]any) any {
		f.t.Fatal("ship must never approve")
		return nil
	})
	add("integration_worker_launch", func(map[string]any) any {
		if !f.worker {
			f.worker, f.unitAlive = true, true
		}
		// Like the real tool, an idempotent replay never shows a later result.
		return map[string]any{"runtime": map[string]any{"launch": map[string]any{"launch_id": strings.Repeat("3", 32)}}}
	})
	add("integration_worker_status", func(map[string]any) any {
		runtime := map[string]any{"launch": map[string]any{"launch_id": strings.Repeat("3", 32)}}
		if f.recorded {
			runtime["worker"] = map[string]any{"result": map[string]any{"state": "EXITED_UNFENCED"}}
		}
		return map[string]any{"runtime": runtime}
	})
	add("integration_worker_fence", func(map[string]any) any {
		if !f.recorded {
			f.t.Fatal("fence before the worker recorded its effect")
		}
		f.unitAlive = false
		return map[string]any{}
	})
	add("integration_recovery_status", func(map[string]any) any {
		if f.released {
			return map[string]any{"state": "RELEASED"}
		}
		return map[string]any{"state": "EFFECT_INTENT"}
	})
	add("integration_recover", func(map[string]any) any {
		if f.unitAlive {
			f.t.Fatal("recover while the worker unit is alive")
		}
		f.released = true
		return map[string]any{"state": "RELEASED"}
	})
	add("integration_publish_main", func(map[string]any) any {
		if !f.released {
			f.t.Fatal("publish before root released the claim")
		}
		f.published = true
		return map[string]any{"state": "PUBLISHED"}
	})
	return r
}

const shipTestCommit = "0123456789abcdef0123456789abcdef01234567"

func shipTestEnv(f *fakeConveyor, now *time.Time) shipEnv {
	return shipEnv{
		Head:    func(context.Context, string, string) (string, error) { return strings.Repeat("e", 40), nil },
		Roadmap: func(string) (string, error) { return strings.Repeat("d", 64), nil },
		Active: func(_ context.Context, unit string) bool {
			return f.unitAlive && unit == "mcpshka-executor-"+strings.Repeat("3", 32)+".service"
		},
		UnitState: func(_ context.Context, unit string) (string, error) {
			if unit != "mcpshka-executor-"+strings.Repeat("2", 32)+".service" {
				f.t.Fatal("wrong evidence unit:", unit)
			}
			if f.testState == "unknown" {
				return "", errors.New("systemd unavailable")
			}
			if f.testState == "" {
				return "active", nil
			}
			return f.testState, nil
		},
		Now: func() time.Time { *now = now.Add(time.Minute); return *now },
	}
}

func TestIntegrationShipWalksTheConveyorAndStopsForReview(t *testing.T) {
	f := &fakeConveyor{t: t, keys: map[string]int{}}
	r := f.registry()
	now := time.Unix(1_800_000_000, 0)
	env := shipTestEnv(f, &now)
	in := IntegrationShipInput{Path: "/root/repos/demo", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32)}
	step := func(want string) IntegrationShipResult {
		t.Helper()
		out, err := r.shipStep(context.Background(), env, in)
		if err != nil || out.Stage != want {
			t.Fatalf("want %s, got %+v %v", want, out, err)
		}
		return out
	}
	step("TEST_PREPARING")
	step("TEST_LAUNCHED")
	step("TEST_RUNNING")
	f.result = "SUCCEEDED"
	step("TEST_FENCING")
	ready := step("READY_FOR_APPROVAL")
	if ready.PermitID != strings.Repeat("1", 32) || !strings.Contains(ready.NextAction, "integration_approve") {
		t.Fatalf("approval hand-off unclear: %+v", ready)
	}
	step("READY_FOR_APPROVAL") // still no approval: nothing advances
	in.ApprovalID = strings.Repeat("b", 64)
	step("WORKER_RUNNING")
	f.unitAlive = false
	done := step("PUBLISHED")
	if !done.Done || done.ApprovalID != in.ApprovalID {
		t.Fatalf("not done: %+v", done)
	}
	if again := step("PUBLISHED"); !again.Done {
		t.Fatal("repeat after success must report done")
	}
	for k, n := range f.keys {
		if n > 1 && (strings.HasPrefix(k, "integration_enqueue|") || strings.HasPrefix(k, "integration_publish_main|")) {
			t.Fatalf("%s sent %d times", k, n)
		}
	}
}

func TestIntegrationShipReportsFailedTestAndBadApproval(t *testing.T) {
	f := &fakeConveyor{t: t, keys: map[string]int{}, enqueued: true, prepared: 5, launched: true, result: "FAILED", fenced: true}
	r := f.registry()
	now := time.Unix(1_800_000_000, 0)
	in := IntegrationShipInput{Path: "/root/repos/demo", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32), Attempt: 2}
	out, err := r.shipStep(context.Background(), shipTestEnv(f, &now), in)
	if err != nil || out.Stage != "TEST_FAILED" || !strings.Contains(out.NextAction, "attempt 3") {
		t.Fatalf("failed test not reported: %+v %v", out, err)
	}
	f.result = "SUCCEEDED"
	in.ApprovalID = "not-an-approval"
	if _, err := r.shipStep(context.Background(), shipTestEnv(f, &now), in); err == nil {
		t.Fatal("malformed approval id accepted")
	}
	if _, err := r.shipStep(context.Background(), shipTestEnv(f, &now), IntegrationShipInput{Path: "/x", Branch: "b", Commit: "HEAD", AdminSessionID: "s"}); err == nil {
		t.Fatal("short commit accepted")
	}
}

func TestIntegrationShipFencesARecordedWorker(t *testing.T) {
	f := &fakeConveyor{t: t, keys: map[string]int{}, result: "SUCCEEDED", launched: true, fenced: true, enqueued: true}
	r := f.registry()
	now := time.Unix(1_800_000_000, 0)
	env := shipTestEnv(f, &now)
	in := IntegrationShipInput{Path: "/root/repos/demo", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32), ApprovalID: strings.Repeat("b", 64)}
	var out IntegrationShipResult
	var err error
	for i := 0; i < 8 && out.Stage != "WORKER_RUNNING"; i++ {
		if out, err = r.shipStep(context.Background(), env, in); err != nil {
			t.Fatal(err)
		}
	}
	if out.Stage != "WORKER_RUNNING" {
		t.Fatalf("worker never started: %+v", out)
	}
	f.recorded = true
	if out, err = r.shipStep(context.Background(), env, in); err != nil || out.Stage != "WORKER_FENCING" {
		t.Fatalf("recorded worker not fenced: %+v %v", out, err)
	}
	if out, err = r.shipStep(context.Background(), env, in); err != nil || out.Stage != "PUBLISHED" {
		t.Fatalf("not published after fence: %+v %v", out, err)
	}
}

func TestIntegrationShipDetectsLostEvidenceWorker(t *testing.T) {
	for _, state := range []string{"inactive", "failed", "active", "activating", "deactivating", "unknown"} {
		t.Run(state, func(t *testing.T) {
			f := &fakeConveyor{t: t, keys: map[string]int{}, enqueued: true, prepared: 1, launched: true, testState: state}
			r := f.registry()
			now := time.Unix(1_800_000_000, 0)
			in := IntegrationShipInput{Path: "/root/repos/demo", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32), Attempt: 2}
			out, err := r.shipStep(context.Background(), shipTestEnv(f, &now), in)
			if err != nil {
				t.Fatal(err)
			}
			want := "TEST_RUNNING"
			if state == "inactive" || state == "failed" {
				want = "TEST_LOST"
			}
			if state == "unknown" {
				want = "TEST_UNKNOWN"
			}
			if out.Stage != want {
				t.Fatalf("want %s: %+v", want, out)
			}
			if want == "TEST_LOST" && (!strings.Contains(out.NextAction, "attempt 3") || !strings.Contains(out.Detail, "without a result")) {
				t.Fatalf("no recovery step: %+v", out)
			}
			for _, call := range f.calls {
				if call == "integration_worker_launch" || call == "integration_publish_main" {
					t.Fatal("lost test advanced integration")
				}
			}
		})
	}
}

func TestIntegrationShipRechecksReceiptAfterUnitExit(t *testing.T) {
	f := &fakeConveyor{t: t, keys: map[string]int{}, enqueued: true, prepared: 1, launched: true, testState: "inactive"}
	r := f.registry()
	exec := r.invokers["exec_start"]
	reads := 0
	r.invokers["exec_start"] = func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, any, error) {
		if args["args"].([]any)[1] == "status" {
			reads++
			if reads == 2 {
				f.result = "SUCCEEDED"
			}
		}
		return exec(ctx, args)
	}
	now := time.Unix(1_800_000_000, 0)
	out, err := r.shipStep(context.Background(), shipTestEnv(f, &now), IntegrationShipInput{Path: "/root/repos/demo", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32)})
	if err != nil || out.Stage != "TEST_FINISHING" || reads != 2 {
		t.Fatalf("receipt race: %+v %v (%d reads)", out, err, reads)
	}
}

func TestIntegrationShipExplainsUnverifiedRecordedResultFence(t *testing.T) {
	f := &fakeConveyor{t: t, keys: map[string]int{}, enqueued: true, prepared: 1, launched: true, result: "SUCCEEDED"}
	r := f.registry()
	original := r.invokers["exec_start"]
	r.invokers["exec_start"] = func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, any, error) {
		if args["args"].([]any)[1] == "canary-fence" {
			return nil, nil, errors.New("FENCE_VERIFICATION_FAILED")
		}
		return original(ctx, args)
	}
	now := time.Unix(1800000000, 0)
	out, err := r.shipStep(context.Background(), shipTestEnv(f, &now), IntegrationShipInput{Path: "/root/repos/demo", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32)})
	if err != nil || out.Stage != "TEST_FENCE_UNVERIFIED" || !strings.Contains(out.NextAction, "canary-recover") || out.Done {
		t.Fatalf("unclassified fence failure: %+v %v", out, err)
	}
}

func TestIntegrationShipLongStepSurvivesClientAndReportsProgress(t *testing.T) {
	r := &toolRegistry{invokers: map[string]toolInvoker{}}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r.invokers["integration_status"] = func(ctx context.Context, _ map[string]any) (*mcp.CallToolResult, any, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return nil, map[string]any{"found": true, "item": map[string]any{"state": "published"}}, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	in := IntegrationShipInput{Path: "/root/repos/demo", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32)}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out, err := r.ship(ctx, shipEnv{}, in)
	if err != nil || out.Stage != "SHIP_RUNNING" || out.Done {
		close(release)
		t.Fatalf("long step lost: %+v %v", out, err)
	}
	<-entered
	// A concurrent poll must observe the same invocation, even after caller cancellation.
	poll, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	out, err = r.ship(poll, shipEnv{}, in)
	if err != nil || out.Stage != "SHIP_RUNNING" || calls.Load() != 1 {
		close(release)
		t.Fatalf("poll relaunched step: %+v %v calls=%d", out, err, calls.Load())
	}
	close(release)
	out, err = r.ship(context.Background(), shipEnv{}, in)
	if err != nil || out.Stage != "PUBLISHED" || !out.Done || calls.Load() != 1 {
		t.Fatalf("background result lost: %+v %v calls=%d", out, err, calls.Load())
	}
}

func TestIntegrationShipReplyBudgetAndIdentityIsolation(t *testing.T) {
	r := &toolRegistry{invokers: map[string]toolInvoker{}}
	release := make(chan struct{})
	defer close(release)
	callers := make(chan string, 2)
	r.invokers["integration_status"] = func(ctx context.Context, _ map[string]any) (*mcp.CallToolResult, any, error) {
		identity, _ := authn.IdentityFromContext(ctx)
		callers <- identity.ID
		select {
		case <-release:
			return nil, map[string]any{"found": true, "item": map[string]any{"state": "published"}}, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	in := IntegrationShipInput{Path: "/root/repos/demo", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32)}
	owner := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner", Role: "operator", Scopes: []string{"*"}})
	start := time.Now()
	out, err := r.ship(owner, shipEnv{}, in)
	if err != nil || out.Stage != "SHIP_RUNNING" || time.Since(start) > 20*time.Second || !strings.Contains(out.Detail, "integration_status") {
		t.Fatalf("reply exceeded budget or lost progress: %+v %v", out, err)
	}
	if got := <-callers; got != "owner" {
		t.Fatalf("identity not preserved: %s", got)
	}
	other, cancel := context.WithTimeout(authn.WithIdentity(context.Background(), authn.Identity{ID: "other", Role: "reader"}), 20*time.Millisecond)
	defer cancel()
	_, err = r.ship(other, shipEnv{}, in)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-callers:
		if got != "other" {
			t.Fatalf("wrong identity: %s", got)
		}
	default:
		t.Fatal("background work shared between identities")
	}
}

// With GitVerse evidence MCPshka publishes and fans out CI itself, waits for
// the run and stops on a failure instead of sealing.
func TestIntegrationShipDrivesCIItself(t *testing.T) {
	f := &fakeConveyor{t: t, keys: map[string]int{}}
	r := f.registry()
	ci := `{"state": "HOLD", "runs": []}`
	var published []string
	lastForge := false
	r.invokers["forge_ci"] = func(_ context.Context, args map[string]any) (*mcp.CallToolResult, any, error) {
		if args["mode"] == "publish" {
			published = append(published, args["provider"].(string))
		}
		lastForge = args["mode"] == "status"
		return nil, map[string]any{"id": "forge-1"}, nil
	}
	r.invokers["job_wait"] = func(context.Context, map[string]any) (*mcp.CallToolResult, any, error) {
		return nil, map[string]any{}, nil
	}
	r.invokers["job_output"] = func(context.Context, map[string]any) (*mcp.CallToolResult, any, error) {
		if lastForge {
			lastForge = false
			return nil, map[string]any{"text": ci + "\n"}, nil
		}
		return nil, map[string]any{"text": f.exec}, nil
	}
	now := time.Unix(1_800_000_000, 0)
	env := shipTestEnv(f, &now)
	in := IntegrationShipInput{Path: "/root/repos/mcpshka", Branch: "feature", Commit: shipTestCommit, AdminSessionID: strings.Repeat("a", 32), Evidence: "gitverse"}
	step := func(want string) IntegrationShipResult {
		t.Helper()
		out, err := r.shipStep(context.Background(), env, in)
		if err != nil || out.Stage != want {
			t.Fatalf("want %s, got %+v %v", want, out, err)
		}
		return out
	}
	if out := step("CI_PUBLISHED"); strings.Join(published, ",") != "gitverse,moshub,github" || !strings.Contains(out.Detail, "moshub") {
		t.Fatalf("fan-out %v %+v", published, out)
	}
	ci = `{"state": "HOLD", "runs": [{"id": 7, "status": "running"}]}`
	step("CI_RUNNING")
	if f.prepared != 0 {
		t.Fatal("sealed before CI passed")
	}
	ci = `{"state": "HOLD", "runs": [{"id": 7}], "failure": {"jobs": [{"excerpt": ["FAIL: TestX"]}]}}`
	if out := step("CI_FAILED"); !strings.Contains(out.Detail, "FAIL: TestX") || !strings.Contains(out.NextAction, "new commit") {
		t.Fatalf("%+v", out)
	}
	ci = `{"state": "observed_pass", "runs": [{"id": 7}]}`
	step("TEST_PREPARING")
}
