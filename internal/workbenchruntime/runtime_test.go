package workbenchruntime

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"mcpshka/internal/recipecontract"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/executor"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
)

func testBinding(t *testing.T, ttl int64) (binding, json.RawMessage) {
	t.Helper()
	b := binding{Schema: runtimeName, JobID: "test-job", Root: "/root/repos/diag", Commit: strings.Repeat("a", 40), TimeoutSeconds: ttl, StartedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	b.Bootstrap = bootstrap(b)
	var err error
	b.Pin, b.Launch, err = recipecontract.RecipePrepareIdentity(b.Bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return b, raw
}

func testSnapshot(b binding) executor.Snapshot {
	identity := executor.Identity{BootID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", InvocationID: strings.Repeat("b", 32), Cgroup: "/system.slice/mcpshka-executor-" + b.Launch + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 123, StartTicks: 4}
	return executor.Snapshot{Pin: b.Pin, Revision: 1, State: "ACTIVE", Launches: []executor.Launch{{ID: b.Launch, State: "REGISTERED", Identity: &identity}}, Source: &executor.SourceSnapshot{Repo: b.Root, Commit: b.Commit, Package: "make:test", Digest: strings.Repeat("c", 64)}}
}

func testManifest(b binding) executor.Manifest {
	return executor.Manifest{Purpose: "project-recipe-test-v1", Pin: b.Pin, Source: testSnapshot(b).Source, Runs: map[string]executor.RunSpec{b.Launch: {Profile: "recipe-test-v1", Command: []string{"make", "test"}, TimeoutSeconds: b.TimeoutSeconds, WorkspaceDevice: 1, WorkspaceInode: 2}}}
}

func reply(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func boundedCall(t *testing.T, ctx context.Context, action string, args []string) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 51*time.Second || action != "executor" || len(args) == 0 {
		t.Fatal("unbounded or foreign broker call", action, args)
	}
}

// Preparation time must not consume a sealed test's execution budget.
func TestPreparationDoesNotConsumeExecutionBudget(t *testing.T) {
	b, raw := testBinding(t, 37)
	s := testSnapshot(b)
	s.Launches = nil
	s.CreatedAt = b.StartedAt.Add(time.Minute)
	launches := 0
	r := &Runtime{now: func() time.Time { return s.CreatedAt }}
	r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "status":
			return reply(t, s), nil
		case "launch":
			launches++
			return []byte("{}"), nil
		default:
			t.Fatalf("freshly sealed permit was fenced: %v", args)
			return nil, nil
		}
	}
	if o, err := r.Observe(raw, false); err != nil || o.State != jobs.Running || launches != 1 {
		t.Fatal("preparation consumed execution budget", o, err, launches)
	}
}

func TestExecutionBudgetUsesDurableLaunchAndQueueStart(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(strconv.FormatBool(queued), func(t *testing.T) {
			b, raw := testBinding(t, 37)
			s := testSnapshot(b)
			s.CreatedAt = b.StartedAt.Add(time.Minute)
			s.Launches[0].ReservedAt = s.CreatedAt.Add(time.Minute)
			start := s.Launches[0].ReservedAt
			s.Queue = &heavyqueue.Status{State: "RUNNING", RunningAt: b.StartedAt}
			if queued {
				s.Queue.RunningAt = start.Add(time.Minute)
				start = s.Queue.RunningAt
			}
			now := start.Add(36 * time.Second)
			fences := 0
			r := &Runtime{now: func() time.Time { return now }}
			r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == "canary-fence" {
					fences++
					s.State = "FENCED"
				} else if args[0] != "status" {
					t.Fatalf("accepted launch replayed: %v", args)
				}
				return reply(t, s), nil
			}
			if o, err := r.Observe(raw, false); err != nil || o.State != jobs.Running || fences != 0 {
				t.Fatal("launch lost execution budget", o, err, fences)
			}
			now = start.Add(37 * time.Second)
			if o, err := r.Observe(raw, false); err != nil || o.State != jobs.TimedOut || !o.ResourcesReleased || fences != 1 {
				t.Fatal("execution deadline not fenced", o, err, fences)
			}
		})
	}
}

func TestStartPinsSourceAndPassesExactRootTTL(t *testing.T) {
	for _, ttl := range []int64{1, 37, 1800, 21600} {
		t.Run(strconv.FormatInt(ttl, 10), func(t *testing.T) {
			b, raw := testBinding(t, ttl)
			calls := [][]string{}
			r := &Runtime{now: func() time.Time { return b.StartedAt }}
			r.call = func(ctx context.Context, action string, args ...string) ([]byte, error) {
				boundedCall(t, ctx, action, args)
				calls = append(calls, append([]string(nil), args...))
				if args[0] == "prepare-recipe" {
					return reply(t, testManifest(b)), nil
				}
				return []byte(`{"state":"LAUNCH_INTENT"}`), nil
			}
			if err := r.Start(raw); err != nil {
				t.Fatal(err)
			}
			want := [][]string{{"prepare-recipe", b.Bootstrap, b.Root, b.Commit, strconv.FormatInt(ttl, 10)}, {"launch", b.Pin.PermitID, b.Launch}}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("wrong bounded root requests: %v", calls)
			}
		})
	}
}

func TestStartRejectsDifferentRootPreparation(t *testing.T) {
	for _, field := range []string{"pin", "source", "repo", "commit", "package", "purpose", "timeout", "launch"} {
		t.Run(field, func(t *testing.T) {
			b, raw := testBinding(t, 60)
			m := testManifest(b)
			switch field {
			case "pin":
				m.Pin.Generation++
			case "source":
				m.Source = nil
			case "repo":
				m.Source.Repo = "/root/repos/other"
			case "commit":
				m.Source.Commit = strings.Repeat("f", 40)
			case "package":
				m.Source.Package = "internal/other"
			case "purpose":
				m.Purpose = "project-test-v1"
			case "timeout":
				spec := m.Runs[b.Launch]
				spec.TimeoutSeconds++
				m.Runs[b.Launch] = spec
			case "launch":
				m.Runs[strings.Repeat("f", 32)] = m.Runs[b.Launch]
				delete(m.Runs, b.Launch)
			}
			calls := 0
			r := &Runtime{call: func(_ context.Context, action string, args ...string) ([]byte, error) {
				calls++
				if action != "executor" || args[0] != "prepare-recipe" {
					t.Fatal("mismatched preparation launched a worker")
				}
				return reply(t, m), nil
			}}
			if err := r.Start(raw); err == nil || calls != 1 {
				t.Fatal("foreign root preparation accepted", err, calls)
			}
		})
	}
}

func TestObserveRejectsDifferentRootSnapshot(t *testing.T) {
	for _, field := range []string{"pin", "source", "repo", "commit", "package", "launch", "multiple-launches"} {
		t.Run(field, func(t *testing.T) {
			b, raw := testBinding(t, 60)
			s := testSnapshot(b)
			switch field {
			case "pin":
				s.Pin.ExecutionID = strings.Repeat("f", 32)
			case "source":
				s.Source = nil
			case "repo":
				s.Source.Repo = "/root/repos/other"
			case "commit":
				s.Source.Commit = strings.Repeat("f", 40)
			case "package":
				s.Source.Package = "internal/other"
			case "launch":
				s.Launches[0].ID = strings.Repeat("f", 32)
			case "multiple-launches":
				s.Launches = append(s.Launches, s.Launches[0])
			}
			r := &Runtime{now: func() time.Time { return b.StartedAt }}
			r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] != "status" {
					t.Fatal("untrusted snapshot caused mutation")
				}
				return reply(t, s), nil
			}
			if o, err := r.Observe(raw, false); err == nil || o.ResourcesReleased || o.State == jobs.Done {
				t.Fatal("different root snapshot accepted", o, err)
			}
		})
	}
}

func TestExitRequiresRootFenceBeforeDone(t *testing.T) {
	for _, fenceOK := range []bool{false, true} {
		t.Run(strconv.FormatBool(fenceOK), func(t *testing.T) {
			b, raw := testBinding(t, 60)
			s := testSnapshot(b)
			s.Result = &executor.ExecutionResult{Pin: b.Pin, LaunchID: b.Launch, State: "EXITED_UNFENCED", ExitCode: 0, Identity: *s.Launches[0].Identity, ObservedAt: b.StartedAt, Trust: "UNTRUSTED", Output: "PASS"}
			calls := []string{}
			r := &Runtime{now: func() time.Time { return b.StartedAt }}
			r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] == "canary-fence" {
					if !fenceOK {
						return nil, errors.New("fence observation lost")
					}
					s.State, s.Launches[0].State, s.Result.State = "FENCED", "FENCED", "SUCCEEDED"
					return []byte(`{}`), nil
				}
				return reply(t, s), nil
			}
			o, err := r.Observe(raw, false)
			if fenceOK {
				if err != nil || o.State != jobs.Done || !o.ResourcesReleased || o.ExitCode == nil || *o.ExitCode != 0 || o.Output != "PASS" || !reflect.DeepEqual(calls, []string{"status", "canary-fence", "status"}) {
					t.Fatal("verified root result not accepted", o, err, calls)
				}
			} else if err == nil || o.State == jobs.Done || o.ResourcesReleased {
				t.Fatal("unfenced exit became success", o, err)
			}
		})
	}
}

func TestLostPreparationReconcilesWithoutRepeatingPrepare(t *testing.T) {
	b, raw := testBinding(t, 60)
	s := testSnapshot(b)
	s.Launches = nil
	calls := []string{}
	r := &Runtime{now: func() time.Time { return b.StartedAt }}
	r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		switch args[0] {
		case "prepare-recipe":
			return nil, errors.New("response lost after seal")
		case "launch":
			s.Launches = []executor.Launch{{ID: b.Launch, State: "LAUNCH_INTENT"}}
			return []byte(`{}`), nil
		case "status":
			return reply(t, s), nil
		default:
			t.Fatal("unexpected mutation", args)
			return nil, nil
		}
	}
	if err := r.Start(raw); err == nil {
		t.Fatal("lost preparation response was not unknown")
	}
	for range 2 {
		if o, err := r.Observe(raw, false); err != nil || o.State != jobs.Running {
			t.Fatal("sealed permit failed reconciliation", o, err)
		}
	}
	if !reflect.DeepEqual(calls, []string{"prepare-recipe", "status", "launch", "status"}) {
		t.Fatal("replayed preparation or one-use launch", calls)
	}
}

func TestLostLaunchReservationNeverReplays(t *testing.T) {
	b, raw := testBinding(t, 60)
	s := testSnapshot(b)
	s.Launches = nil
	launches := 0
	r := &Runtime{now: func() time.Time { return b.StartedAt }}
	r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "launch" {
			launches++
			s.Launches = []executor.Launch{{ID: b.Launch, State: "LAUNCH_INTENT"}}
			return nil, errors.New("response lost after root Reserve")
		}
		if args[0] != "status" {
			t.Fatal("observation repeated preparation")
		}
		return reply(t, s), nil
	}
	if _, err := r.Observe(raw, false); err == nil {
		t.Fatal("lost reservation response not reported")
	}
	if o, err := r.Observe(raw, false); err != nil || o.State != jobs.Running || launches != 1 {
		t.Fatal("accepted root reservation replayed", o, err, launches)
	}
}

func TestCancelAndDeadlineWithoutResultNeverInventDone(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancel), func(t *testing.T) {
			b, raw := testBinding(t, 37)
			s := testSnapshot(b)
			s.Launches = nil
			now := b.StartedAt.Add(37 * time.Second)
			if cancel {
				now = b.StartedAt
			}
			calls := []string{}
			r := &Runtime{now: func() time.Time { return now }}
			r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] == "canary-fence" {
					s.State = "FENCED"
				} else if args[0] != "status" {
					t.Fatal("expired/cancelled empty permit launched", args)
				}
				return reply(t, s), nil
			}
			o, err := r.Observe(raw, cancel)
			want, code := jobs.TimedOut, 124
			if cancel {
				want, code = jobs.Cancelled, 130
			}
			if err != nil || o.State != want || !o.ResourcesReleased || o.ExitCode == nil || *o.ExitCode != code || o.Error == "" || !reflect.DeepEqual(calls, []string{"status", "canary-fence", "status"}) {
				t.Fatal("missing result fabricated success or lost fence", o, err, calls)
			}
		})
	}
}

func TestMCP232SealedWorktreeAdmitsOnlyVerifiedGitLinks(t *testing.T) {
	for _, bad := range []string{
		"/root/repos/wt/nonexistent-agent-worktree",
		"/root/repos/wt/not-an-immediate/repo",
		"/tmp/foreign-worktree",
		"/root/repos/diag/inner",
		"/root/repos/wt/../diag",
	} {
		if sealedProjectRoot(bad) {
			t.Fatalf("untrusted sealed scope accepted: %q", bad)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Hosted CI checks out outside the managed root. Exercise the same
	// canonical contract there and verify the actual linked worktree on H96.
	current := filepath.Dir(filepath.Dir(wd))
	root := "/root/repos/mcpshka"
	if filepath.Dir(current) == "/root/repos/wt" {
		root = current
	}
	if !sealedProjectRoot(root) {
		t.Fatalf("managed Git checkout not accepted as sealed source: %q", root)
	}
	resolved := projectprofile.Resolution{Root: root, Source: "registered", Profile: projectprofile.Profile{
		Name: "fixture", Workflows: map[string]projectprofile.Workflow{
			"test": {Command: "sh", Args: []string{"tests/run_safe.sh"}},
		},
	}}
	r := &Runtime{resolve: func(path string) (projectprofile.Resolution, error) {
		if path != root {
			return projectprofile.Resolution{}, errors.New("foreign root")
		}
		return resolved, nil
	}}
	if !r.Match("sh", []string{"tests/run_safe.sh"}, root) {
		t.Fatal("registered worktree test was not admitted to sealed runtime")
	}
	repo, err := sealedSnapshotRepository(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(repo) != "/root/repos" {
		t.Fatalf("broker would refuse snapshot repository: %q", repo)
	}
	b, _ := testBinding(t, 60)
	b.Root = repo
	b.Argv = []string{"sh", "tests/run_safe.sh"}
	workflowArgs := []string{"test", projectprofile.ProfileDigest(resolved.Profile), b.Commit}
	if !r.Match(WorkflowCommand, workflowArgs, root) {
		t.Fatal("registered workflow failed sealed admission")
	}
	if filepath.Dir(root) == "/root/repos/wt" && r.Match(StageCommand, workflowArgs, root) {
		t.Fatal("pipeline stage unexpectedly expanded to nested worktrees")
	}
	if args := b.recipeArgs("prepare-recipe"); args[2] != repo || args[3] != b.Commit {
		t.Fatalf("recipe source or revision differs: %v", args)
	}
	b.Bootstrap = bootstrap(b)
	b.Pin, b.Launch, err = recipecontract.RecipePrepareIdentity(b.Bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decode(reply(t, b), false); err != nil {
		t.Fatal("valid current sealed worktree binding rejected:", err)
	}
}

func TestRegisteredProfileSealsWholeTreeWithItsArgv(t *testing.T) {
	registered := projectprofile.Resolution{Root: "/root/repos/lane", Source: "registered", Profile: projectprofile.Profile{Name: "lane", Workflows: map[string]projectprofile.Workflow{"test": {Command: "sh", Args: []string{"tests/run_safe.sh"}}}}}
	r := &Runtime{resolve: func(string) (projectprofile.Resolution, error) { return registered, nil }}
	if !r.Match("sh", []string{"tests/run_safe.sh"}, "/root/repos/lane") {
		t.Fatal("registered test workflow not sealed")
	}
	for _, c := range []struct {
		command string
		args    []string
		root    string
	}{{"sh", []string{"other.sh"}, "/root/repos/lane"}, {"bash", []string{"tests/run_safe.sh"}, "/root/repos/lane"}, {"sh", []string{"tests/run_safe.sh"}, "/root/repos/lane/sub"}} {
		if r.Match(c.command, c.args, c.root) {
			t.Fatal("non-workflow command sealed", c)
		}
	}
	builtin := registered
	builtin.Source = "builtin"
	r.resolve = func(string) (projectprofile.Resolution, error) { return builtin, nil }
	if r.Match("sh", []string{"tests/run_safe.sh"}, "/root/repos/lane") {
		t.Fatal("non-DIAG builtin sealed as a tree")
	}

	b, _ := testBinding(t, 60)
	b.Argv = []string{"sh", "tests/run_safe.sh"}
	b.Bootstrap = bootstrap(b)
	b.Pin, b.Launch, _ = recipecontract.RecipePrepareIdentity(b.Bootstrap)
	raw := reply(t, b)
	if _, err := decode(raw, false); err != nil {
		t.Fatal(err)
	}
	m := testManifest(b)
	m.Source.Package = executor.TreeTestPackage
	spec := m.Runs[b.Launch]
	spec.Command = b.Argv
	m.Runs[b.Launch] = spec
	calls := [][]string{}
	r.now = func() time.Time { return b.StartedAt }
	r.call = func(ctx context.Context, action string, args ...string) ([]byte, error) {
		boundedCall(t, ctx, action, args)
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "prepare-recipe" {
			return reply(t, m), nil
		}
		return []byte(`{}`), nil
	}
	if err := r.Start(raw); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare-recipe", b.Bootstrap, b.Root, b.Commit, "60", "sh", "tests/run_safe.sh"}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatal("tree argv not pinned into root preparation", calls)
	}
	legacy, _ := testBinding(t, 60)
	if bootstrap(legacy) == b.Bootstrap {
		t.Fatal("argv not bound into bootstrap identity")
	}
}

func TestPermitlessPreparationFailsOnlyWithRootAbandonProof(t *testing.T) {
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		reply   string
		failed  bool
	}{
		{"inside-window", time.Minute, "ABANDONED", false},
		{"root-refused", prepareWindow + time.Minute, "", false},
		{"foreign-permit", prepareWindow + time.Minute, "FOREIGN", false},
		{"abandoned", prepareWindow + time.Minute, "ABANDONED", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := testBinding(t, 60)
			b.Attempt = maxPrepareAttempts - 1 // retries exhausted: the proof ends the job
			raw, _ := json.Marshal(b)
			calls := [][]string{}
			r := &Runtime{now: func() time.Time { return b.StartedAt.Add(tc.elapsed) }}
			r.call = func(ctx context.Context, action string, args ...string) ([]byte, error) {
				boundedCall(t, ctx, action, args)
				calls = append(calls, append([]string(nil), args...))
				switch {
				case args[0] == "status" || args[0] == "recipe-status":
					return nil, executor.ErrHold
				case args[0] == "abandon-recipe" && tc.reply == "ABANDONED":
					return reply(t, map[string]string{"state": "ABANDONED", "permit_id": b.Pin.PermitID}), nil
				case args[0] == "abandon-recipe" && tc.reply == "FOREIGN":
					return reply(t, map[string]string{"state": "ABANDONED", "permit_id": strings.Repeat("f", 32)}), nil
				case args[0] == "abandon-recipe":
					return nil, executor.ErrHold
				}
				t.Fatal("permitless preparation caused a mutation", args)
				return nil, nil
			}
			o, err := r.Observe(raw, false)
			if tc.failed {
				if err != nil || o.State != jobs.Failed || !o.ResourcesReleased || o.ExitCode == nil || *o.ExitCode != 1 {
					t.Fatal("proven permitless preparation stayed unresolved", o, err)
				}
				want := []string{"abandon-recipe", b.Bootstrap, b.Root, b.Commit, "60"}
				if !reflect.DeepEqual(calls[len(calls)-1], want) {
					t.Fatal("abandon request not pinned to the binding", calls)
				}
				return
			}
			if err == nil || o.ResourcesReleased || o.State == jobs.Failed {
				t.Fatal("unproven permitless preparation resolved", o, err)
			}
			if tc.elapsed < prepareWindow && (len(calls) != 2 || calls[1][0] != "recipe-status") {
				t.Fatal("abandon probed while the constructor may still run", calls)
			}
		})
	}
}

func TestWholeTreePreparationCannotBeAbandonedAtLegacyDeadline(t *testing.T) {
	b, _ := testBinding(t, 60)
	b.Argv = []string{"go", "test", "./..."}
	b.Bootstrap = bootstrap(b)
	b.Pin, b.Launch, _ = recipecontract.RecipePrepareIdentity(b.Bootstrap)
	raw := reply(t, b)
	now := b.StartedAt.Add(prepareWindow + time.Second)
	r := &Runtime{now: func() time.Time { return now }}
	abandoned := false
	r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "status", "recipe-status":
			return nil, executor.ErrHold
		case "abandon-recipe":
			abandoned = true
			return reply(t, map[string]string{"state": "ABANDONED", "permit_id": b.Pin.PermitID}), nil
		default:
			t.Fatalf("unexpected call: %v", args)
			return nil, nil
		}
	}
	b.Attempt = maxPrepareAttempts - 1
	raw = reply(t, b)
	if o, err := r.Observe(raw, false); err == nil || o.ResourcesReleased || abandoned {
		t.Fatal("whole-tree preparation was abandoned early", o, err)
	}
	now = b.StartedAt.Add(prepareWindowFor(b) + time.Second)
	if o, err := r.Observe(raw, false); err != nil || o.State != jobs.Failed || !o.ResourcesReleased || !abandoned {
		t.Fatal("whole-tree preparation did not reconcile after root proof", o, err)
	}
}

func TestRemovedNestedWorktreeCanReconcileWithoutRelaunch(t *testing.T) {
	b, _ := testBinding(t, 60)
	b.Root = "/root/repos/wt/mcpshka-removed-recovery-test"
	b.Bootstrap = bootstrap(b)
	var err error
	b.Pin, b.Launch, err = recipecontract.RecipePrepareIdentity(b.Bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}

	for _, hasPermit := range []bool{false, true} {
		calls := 0
		r := &Runtime{now: func() time.Time { return b.StartedAt.Add(prepareWindow + time.Minute) }}
		r.call = func(_ context.Context, family string, args ...string) ([]byte, error) {
			calls++
			if family != "executor" {
				t.Fatalf("unexpected broker family: %s", family)
			}
			switch args[0] {
			case "status":
				if hasPermit {
					return reply(t, testSnapshot(b)), nil
				}
				return nil, executor.ErrHold
			case "abandon-recipe":
				if hasPermit || !reflect.DeepEqual(args, b.recipeArgs("abandon-recipe")) {
					t.Fatalf("unsafe abandon request: %v", args)
				}
				return reply(t, map[string]string{"state": "ABANDONED", "permit_id": b.Pin.PermitID}), nil
			default:
				t.Fatalf("legacy recovery attempted %v", args)
				return nil, nil
			}
		}
		if err := r.Start(raw); err == nil || calls != 0 {
			t.Fatalf("legacy preparation was admitted: calls=%d err=%v", calls, err)
		}
		got, err := r.Observe(raw, false)
		if hasPermit {
			if err == nil || calls != 1 || got.ResourcesReleased {
				t.Fatalf("existing legacy permit was not held: calls=%d observation=%+v err=%v", calls, got, err)
			}
		} else if err != nil || calls != 2 || !got.ResourcesReleased || got.State != jobs.Failed {
			t.Fatalf("legacy preparation did not reconcile: calls=%d observation=%+v err=%v", calls, got, err)
		}
	}
}

func TestMalformedBindingFailsBeforeBroker(t *testing.T) {
	_, valid := testBinding(t, 60)
	for _, field := range []string{"unknown", "trailing", "pin", "bootstrap", "commit", "root", "timeout", "started"} {
		t.Run(field, func(t *testing.T) {
			b, _ := testBinding(t, 60)
			var raw []byte
			switch field {
			case "pin":
				b.Pin.Generation++
			case "bootstrap":
				b.Bootstrap = strings.Repeat("f", 32)
			case "commit":
				b.Commit = strings.Repeat("z", 40)
			case "root":
				b.Root = "/root/repos/diag/nested"
			case "timeout":
				b.TimeoutSeconds = 21601
			case "started":
				b.StartedAt = time.Time{}
			}
			raw = reply(t, b)
			if field == "unknown" {
				raw = append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"unexpected":true}`)...)
			} else if field == "trailing" {
				raw = append(append([]byte(nil), valid...), []byte(` {}`)...)
			}
			r := &Runtime{call: func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("malformed binding reached root broker")
				return nil, nil
			}}
			if err := r.Start(raw); err == nil {
				t.Fatal("malformed start binding accepted")
			}
			if _, err := r.Observe(raw, false); err == nil {
				t.Fatal("malformed observation binding accepted")
			}
		})
	}
}

func TestPlanRejectsScopeBeforeRepositoryHooks(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".git", ".git/objects", ".git/refs"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(t.TempDir(), "hook-executed")
	hook := filepath.Join(t.TempDir(), "fsmonitor")
	files := map[string]string{
		"go.mod":      "module gitverse.ru/dmitriy_1ebedev/diag\n\ngo 1.26\n",
		"Makefile":    "test:\n\t@true\n",
		".git/HEAD":   "ref: refs/heads/main\n",
		".git/config": "[core]\nrepositoryformatversion = 0\nbare = false\nfsmonitor = " + hook + "\n[filter \"fixture\"]\nclean = touch " + sentinel + "\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+sentinel+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// An empty index lets status consult fsmonitor without any Git mutation.
	index := make([]byte, 12)
	copy(index, "DIRC")
	binary.BigEndian.PutUint32(index[4:8], 2)
	sum := sha1.Sum(index)
	if err := os.WriteFile(filepath.Join(root, ".git/index"), append(index, sum[:]...), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(&policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}})
	if _, err := r.Plan("test-job", "make", []string{"test"}, root, jobs.StartOptions{Timeout: time.Minute}); err == nil {
		t.Fatal("non-immediate repository admitted")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("scope/profile inspection executed a repository hook before sandbox admission")
	}
}

func TestQueuedTimeDoesNotConsumeRecipeExecutionBudget(t *testing.T) {
	b, raw := testBinding(t, 60)
	s := testSnapshot(b)
	s.Queue = &heavyqueue.Status{State: "QUEUED", Reason: "host load", Position: 3}
	r := &Runtime{now: func() time.Time { return b.StartedAt.Add(time.Hour) }}
	r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] != "status" {
			t.Fatalf("queued test fenced or relaunched: %v", args)
		}
		return reply(t, s), nil
	}
	o, err := r.Observe(raw, false)
	if err != nil || o.State != jobs.Queued || o.Queue == nil || o.Queue.Position != 3 {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestAsyncPreparationNeverLaunchesBeforeSeal(t *testing.T) {
	b, raw := testBinding(t, 60)
	pending := recipecontract.RecipePrepareState{State: "PREPARING", PermitID: b.Pin.PermitID, LaunchID: b.Launch}
	calls := []string{}
	sealed := false
	r := &Runtime{now: func() time.Time { return b.StartedAt }}
	r.call = func(ctx context.Context, action string, args ...string) ([]byte, error) {
		boundedCall(t, ctx, action, args)
		calls = append(calls, args[0])
		switch args[0] {
		case "prepare-recipe", "recipe-status":
			return reply(t, pending), nil
		case "status":
			if !sealed {
				return nil, errors.New("permit not yet sealed")
			}
			s := testSnapshot(b)
			s.Launches = nil
			return reply(t, s), nil
		case "launch":
			if !sealed {
				t.Fatal("launch before seal")
			}
			return []byte("{}"), nil
		default:
			t.Fatalf("unexpected action %s", args[0])
			return nil, nil
		}
	}
	if err := r.Start(raw); err != nil {
		t.Fatal(err)
	}
	for _, cancel := range []bool{false, true} {
		o, err := r.Observe(raw, cancel)
		if err != nil || o.State != jobs.Running || o.ResourcesReleased {
			t.Fatalf("pending: %+v %v", o, err)
		}
	}
	if !reflect.DeepEqual(calls, []string{"prepare-recipe", "status", "recipe-status", "status", "recipe-status"}) {
		t.Fatalf("preparation replayed or launched: %v", calls)
	}
	sealed = true
	if o, err := r.Observe(raw, false); err != nil || o.State != jobs.Running {
		t.Fatalf("sealed: %+v %v", o, err)
	}
	if calls[len(calls)-1] != "launch" {
		t.Fatal("sealed test was not launched")
	}
}

func TestAsyncPreparationRejectsForeignIdentity(t *testing.T) {
	for _, field := range []string{"permit", "launch", "state"} {
		b, raw := testBinding(t, 60)
		p := recipecontract.RecipePrepareState{State: "PREPARING", PermitID: b.Pin.PermitID, LaunchID: b.Launch}
		switch field {
		case "permit":
			p.PermitID = strings.Repeat("f", 32)
		case "launch":
			p.LaunchID = strings.Repeat("f", 32)
		case "state":
			p.State = "FAILED"
		}
		r := &Runtime{now: func() time.Time { return b.StartedAt }}
		r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] != "prepare-recipe" {
				t.Fatal("unverified preparation caused launch")
			}
			return reply(t, p), nil
		}
		if r.Start(raw) == nil {
			t.Fatalf("accepted foreign %s", field)
		}
	}
}

func TestStartAcceptsConcurrentSealWithoutLaunch(t *testing.T) {
	b, raw := testBinding(t, 60)
	r := &Runtime{call: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] != "prepare-recipe" {
			t.Fatal("scheduling observation caused launch")
		}
		return reply(t, recipecontract.RecipePrepareState{State: "SEALED", PermitID: b.Pin.PermitID, LaunchID: b.Launch}), nil
	}}
	if err := r.Start(raw); err != nil {
		t.Fatal(err)
	}
}

// 08.11: a preparation that expired on a loaded H96 failed the whole run;
// with root's proof that nothing ran, the job prepares again by itself.
func TestNoEffectPreparationRetriesUnderFreshIdentity(t *testing.T) {
	b, raw := testBinding(t, 60)
	calls := [][]string{}
	r := &Runtime{now: func() time.Time { return b.StartedAt.Add(prepareWindow + time.Minute) }}
	r.call = func(ctx context.Context, action string, args ...string) ([]byte, error) {
		boundedCall(t, ctx, action, args)
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "status", "recipe-status":
			return nil, executor.ErrHold
		case "abandon-recipe":
			return reply(t, map[string]string{"state": "ABANDONED", "permit_id": b.Pin.PermitID}), nil
		case "prepare-recipe":
			pin, launch, _ := recipecontract.RecipePrepareIdentity(args[1])
			return reply(t, recipecontract.RecipePrepareState{State: "PREPARING", PermitID: pin.PermitID, LaunchID: launch}), nil
		}
		t.Fatal("unexpected root call", args)
		return nil, nil
	}
	o, err := r.Observe(raw, false)
	if err != nil || o.State != jobs.Running || len(o.Rebind) == 0 {
		t.Fatal("no-effect preparation was not retried", o, err)
	}
	next, err := decode(o.Rebind, false)
	if err != nil || next.Attempt != 1 || next.Bootstrap == b.Bootstrap || next.JobID != b.JobID || next.Commit != b.Commit {
		t.Fatal("re-preparation identity", next, err)
	}
	last := calls[len(calls)-1]
	if last[0] != "prepare-recipe" || last[1] != next.Bootstrap {
		t.Fatal("re-preparation not pinned to the new identity", calls)
	}
}

// 08.11: a full suite still busy at its budget was cut at 1800 s; it now
// runs on while its unit burns CPU and is fenced once it stalls.
func TestBudgetExtendsWhileUnitProgresses(t *testing.T) {
	b, raw := testBinding(t, 37)
	s := testSnapshot(b)
	start := s.Launches[0].ReservedAt
	if start.IsZero() {
		start = b.StartedAt
	}
	now := start.Add(time.Minute)
	cpu := uint64(1)
	fences := 0
	r := &Runtime{now: func() time.Time { return now }, cpuUsage: func(unit string) (uint64, error) {
		if unit != "mcpshka-executor-"+b.Launch+".service" {
			t.Fatal("foreign unit sampled", unit)
		}
		return cpu, nil
	}}
	r.call = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "canary-fence" {
			fences++
			s.State = "FENCED"
		}
		return reply(t, s), nil
	}
	for i := 0; i < 3; i++ {
		cpu += 1000
		now = now.Add(time.Minute)
		if o, err := r.Observe(raw, false); err != nil || o.State != jobs.Running || fences != 0 {
			t.Fatal("progressing suite fenced at its budget", o, err, fences)
		}
	}
	now = now.Add(progressWindow + time.Second)
	if o, err := r.Observe(raw, false); err != nil || o.State != jobs.TimedOut || fences != 1 {
		t.Fatal("stalled suite not fenced", o, err, fences)
	}
}
