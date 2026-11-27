package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"mcpshka/internal/gitops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/roadmapscan"
	"mcpshka/internal/statestore"
)

func canary(t *testing.T, fail string) (*Engine, *jobs.Manager, string, string) {
	t.Helper()
	root, state := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module canary.test/workflow\n\ngo 1.26\n"), 0600)
	os.WriteFile(filepath.Join(root, "ROADMAP.md"), []byte("## P7 — Canary\n- [ ] execution\n"), 0600)
	os.Mkdir(filepath.Join(root, ".mcpshka"), 0700)
	stages := []string{"inspect", "modify", "test", "review", "commit", "build", "deploy", "reconnect", "verify", "cleanup"}
	profile := projectprofile.Profile{Schema: projectprofile.SchemaV1, Name: "canary", Match: projectprofile.Match{Module: "canary.test/workflow"}, Workflows: map[string]projectprofile.Workflow{}, Pipelines: map[string]projectprofile.Pipeline{"release": {Stages: stages, Rollback: "rollback", Finalizer: "finalizer"}}}
	for _, name := range append(stages, "rollback", "finalizer") {
		code := "from pathlib import Path; p=Path('events'); p.write_text((p.read_text() if p.exists() else '')+'" + name + "\\n')"
		if name == fail {
			code += "; raise SystemExit(1)"
		}
		profile.Workflows[name] = projectprofile.Workflow{Command: "python3", Args: []string{"-c", code}, TimeoutSeconds: 5, OutputLimitBytes: 1024}
	}
	raw, _ := json.Marshal(profile)
	os.WriteFile(filepath.Join(root, ".mcpshka/profile.json"), raw, 0600)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Canary", "-c", "user.email=canary@localhost", "commit", "-qm", "canary"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	if _, err := statestore.Open(filepath.Join(state, "state.db")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600)
	p := &policy.Policy{AllowedWorkdirs: []string{root}, DeniedCommands: map[string]struct{}{}}
	manager, err := jobs.New(filepath.Join(state, "jobs"), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(time.Second) })
	scan, err := roadmapscan.Load(filepath.Join(root, "ROADMAP.md"), "")
	if err != nil {
		t.Fatal(err)
	}
	engine := New(manager)
	manager.SetLeaseGuard(engine.Guard)
	return engine, manager, root, scan.SHA256
}

func finish(t *testing.T, e *Engine, id string) Run {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r, err := e.Tick(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == "done" || r.State == "rolled_back" || r.State == "hold" {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("workflow did not settle")
	return Run{}
}

func TestPipelineSurvivesColdSessionAndNeverDuplicatesStages(t *testing.T) {
	e, m, root, sha := canary(t, "")
	first, err := e.Start(root, "release", "canary-run", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == "" || first.Scope != "P7" || first.RoadmapSHA != sha {
		t.Fatalf("missing pins: %+v", first)
	}
	if _, err = e.Start(root, "release", "second-owner", sha, "P7"); err == nil {
		t.Fatal("accepted a second worktree writer")
	}
	if _, err = e.Tick(first.ID); err != nil {
		t.Fatal(err)
	}
	// Restart the manager after a terminal stage receipt and reload from disk.
	launched, err := e.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err := m.Get(launched.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == jobs.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first stage did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := m.Close(time.Second); err != nil {
		t.Fatal(err)
	}
	restarted, err := jobs.New(m.StateDir(), m.Policy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close(time.Second) })
	cold := New(restarted)
	restarted.SetLeaseGuard(cold.Guard)
	if _, err = cold.Start(root, "release", first.ID, sha, "P7"); err != nil {
		t.Fatal(err)
	}
	done := finish(t, cold, first.ID)
	if done.State != "done" {
		t.Fatalf("workflow failed: %+v", done)
	}
	if again, err := cold.Tick(first.ID); err != nil || again.State != "done" {
		t.Fatalf("terminal replay: %+v %v", again, err)
	}
	events, _ := os.ReadFile(filepath.Join(root, "events"))
	if string(events) != "inspect\nmodify\ntest\nreview\ncommit\nbuild\ndeploy\nreconnect\nverify\ncleanup\n" {
		t.Fatalf("duplicate or skipped stage: %s", events)
	}
}

func TestLiveDescendantPreventsAdvanceAndUnlock(t *testing.T) {
	e, m, root, sha := canary(t, "")
	path := filepath.Join(root, ".mcpshka/profile.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var profile projectprofile.Profile
	if err = json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	w := profile.Workflows["inspect"]
	w.Args[1] += "; import subprocess; subprocess.Popen(['python3','-c','import time; time.sleep(20)'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)"
	profile.Workflows["inspect"] = w
	raw, err = json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".mcpshka/profile.json"}, {"-c", "user.name=Canary", "-c", "user.email=canary@localhost", "commit", "-qm", "descendant fixture"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %s %v", out, err)
		}
	}
	r, err := e.Start(root, "release", "descendant-canary", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	r, err = e.Tick(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.Get(r.JobID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-job.PID, syscall.SIGKILL) }) // Only this test's private process group.
	held := finish(t, e, r.ID)
	if held.State != "hold" || !strings.Contains(held.Reason, "resources") {
		t.Fatalf("live writer accepted: %+v", held)
	}
	if err = e.Guard(root, ""); err == nil {
		t.Fatal("unlocked a live writer")
	}
	events, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil || string(events) != "inspect\n" {
		t.Fatalf("advanced with child alive: %s %v", events, err)
	}
}

func TestLostExitAfterRestartCompensatesWithoutInventingSuccess(t *testing.T) {
	e, m, root, sha := canary(t, "")
	r, err := e.Start(root, "release", "unknown-exit", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	r, err = e.Tick(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var job jobs.Snapshot
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err = m.Get(r.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == jobs.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stage did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = m.Close(time.Second); err != nil {
		t.Fatal(err)
	}
	// Model a lost exit receipt: disk still records the pre-exit running identity.
	job.State = jobs.Running
	job.ExitCode = nil
	job.FinishedAt = time.Time{}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.db.Put("jobs/"+job.ID+"/metadata.json", raw); err != nil {
		t.Fatal(err)
	}
	restarted, err := jobs.New(m.StateDir(), m.Policy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close(time.Second) })
	cold := New(restarted)
	restarted.SetLeaseGuard(cold.Guard)
	done := finish(t, cold, r.ID)
	if done.State != "rolled_back" {
		t.Fatalf("unknown recovery: %+v", done)
	}
	original, err := restarted.Get(job.ID)
	if err != nil || original.State != jobs.Unknown || !original.ResourcesReleased || original.ExitCode != nil {
		t.Fatalf("fabricated exit: %+v %v", original, err)
	}
	events, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil || string(events) != "inspect\nrollback\nfinalizer\n" {
		t.Fatalf("replayed mutation: %s %v", events, err)
	}
}

func TestCanaryModifiesCommitsBuildsDeploysAndVerifies(t *testing.T) {
	e, _, root, sha := canary(t, "")
	script := `import pathlib, subprocess, sys
p=pathlib.Path('.')
stage=sys.argv[1]
def git(*args): return subprocess.check_output(['git',*args], text=True)
if stage=='inspect': assert git('status','--porcelain')==''
elif stage=='modify': (p/'app.py').write_text('def value(): return 42\n')
elif stage in ('test','review'):
    ns={}; exec(compile((p/'app.py').read_text(),'app.py','exec'),ns); assert ns['value']()==42
elif stage=='commit': git('-c','user.name=Canary','-c','user.email=canary@localhost','commit','-qam','verified canary')
elif stage=='build': (p/'artifact.py').write_bytes((p/'app.py').read_bytes())
elif stage=='deploy':
    (p/'installed').mkdir(exist_ok=True); (p/'installed/active.py').write_bytes((p/'artifact.py').read_bytes())
elif stage in ('reconnect','verify'):
    ns={}; exec(compile((p/'installed/active.py').read_text(),'active.py','exec'),ns); assert ns['value']()==42
    assert git('status','--porcelain')==''
elif stage=='cleanup': (p/'artifact.py').unlink()
elif stage=='rollback': (p/'installed/active.py').unlink(missing_ok=True)
elif stage=='finalizer': (p/'artifact.py').unlink(missing_ok=True)
else: raise SystemExit('unknown stage')
events=p/'events'; events.write_text((events.read_text() if events.exists() else '')+stage+'\n')
`
	for name, data := range map[string]string{"canary.py": script, "app.py": "def value(): return 0\n", ".gitignore": "events\nartifact.py\ninstalled/\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, ".mcpshka/profile.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var profile projectprofile.Profile
	if err = json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	for name, w := range profile.Workflows {
		w.Args = []string{"canary.py", name}
		profile.Workflows[name] = w
	}
	raw, err = json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Canary", "-c", "user.email=canary@localhost", "commit", "-qm", "semantic canary seed"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("seed: %s %v", out, err)
		}
	}
	r, err := e.Start(root, "release", "semantic-canary", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	done := finish(t, e, r.ID)
	if done.State != "done" {
		t.Fatalf("canary failed: %+v", done)
	}
	rev, err := gitops.New(e.manager.Policy()).Revision(root)
	if err != nil || rev == r.Revision {
		t.Fatal("canary did not commit")
	}
	installed, err := os.ReadFile(filepath.Join(root, "installed/active.py"))
	if err != nil || string(installed) != "def value(): return 42\n" {
		t.Fatalf("deployed artifact: %s %v", installed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "artifact.py")); !os.IsNotExist(err) {
		t.Fatal("cleanup missing")
	}
	if err = e.Guard(root, ""); err != nil {
		t.Fatalf("canary claim not released: %v", err)
	}
}

func TestFailedPipelineCompensatesAndFinalizesOnce(t *testing.T) {
	stages := []string{"inspect", "modify", "test", "review", "commit", "build", "deploy", "reconnect", "verify", "cleanup"}
	for index, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			e, _, root, sha := canary(t, stage)
			r, err := e.Start(root, "release", "failed-canary", sha, "P7")
			if err != nil {
				t.Fatal(err)
			}
			r = finish(t, e, r.ID)
			if r.State != "rolled_back" {
				t.Fatalf("compensation failed: %+v", r)
			}
			for i := 0; i < 2; i++ {
				if _, err = e.Tick(r.ID); err != nil {
					t.Fatal(err)
				}
			}
			events, _ := os.ReadFile(filepath.Join(root, "events"))
			if string(events) != strings.Join(stages[:index+1], "\n")+"\nrollback\nfinalizer\n" {
				t.Fatalf("unsafe recovery: %s", events)
			}
		})
	}
}

func TestRoadmapDriftHoldsWithoutLaunching(t *testing.T) {
	e, _, root, sha := canary(t, "")
	r, err := e.Start(root, "release", "drift-canary", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "ROADMAP.md"), []byte("## P7 — Changed\n- [ ] execution\n"), 0600)
	held, err := e.Tick(r.ID)
	if err != nil || held.State != "hold" || !strings.Contains(held.Reason, "roadmap") {
		t.Fatalf("drift was not held: %+v %v", held, err)
	}
	if _, err = os.Stat(filepath.Join(root, "events")); !os.IsNotExist(err) {
		t.Fatal("executed after roadmap drift")
	}
}

func TestLostLaunchReceiptNeverReplaysOrUnlocks(t *testing.T) {
	e, _, root, sha := canary(t, "")
	r, err := e.Start(root, "release", "lost-receipt", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	claimed := r
	claimed.LaunchClaimed = true
	claimed.JobID = stageJobID(r.ID, r.Index)
	claimed.LaunchAt = time.Now().Add(-time.Minute)
	if _, err = e.save(r, claimed); err != nil {
		t.Fatal(err)
	}
	held, err := e.Tick(r.ID)
	if err != nil || held.State != "hold" {
		t.Fatalf("lost receipt: %+v %v", held, err)
	}
	if err = e.Guard(root, claimed.JobID); err == nil {
		t.Fatal("held run admitted a job")
	}
	if _, err = e.Start(root, "release", "another-run", sha, "P7"); err == nil {
		t.Fatal("unknown outcome unlocked worktree")
	}
	if _, err = os.Stat(filepath.Join(root, "events")); !os.IsNotExist(err) {
		t.Fatal("replayed an uncertain mutation")
	}
}

func TestGatedMissingReceiptAutomaticallyCompensatesWithoutReplay(t *testing.T) {
	for _, launched := range []bool{false, true} {
		t.Run(fmt.Sprint(launched), func(t *testing.T) {
			e, m, root, sha := canary(t, "")
			// A real completed process supplies a non-reusable dead identity.
			probe, err := m.Start("sleep", []string{"0.05"}, root)
			if err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(5 * time.Second); ; {
				got, err := m.Get(probe.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.State == jobs.Done {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("probe pending")
				}
				time.Sleep(10 * time.Millisecond)
			}
			r, err := e.Start(root, "release", "missing-gated", sha, "P7")
			if err != nil {
				t.Fatal(err)
			}
			if launched {
				r, err = e.Tick(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				for deadline := time.Now().Add(5 * time.Second); ; {
					got, err := m.Get(r.JobID)
					if err != nil {
						t.Fatal(err)
					}
					if got.State == jobs.Done {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("stage pending")
					}
					time.Sleep(10 * time.Millisecond)
				}
				key := "jobs/" + r.JobID + "/metadata.json"
				raw, err := e.db.Get(key)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = e.db.Delete(key, raw); err != nil {
					t.Fatal(err)
				}
			} else {
				n := r
				n.LaunchClaimed = true
				n.JobID = stageJobID(r.ID, r.Index)
				n.LaunchProtocol = jobs.ManagedLaunchProtocol
				n.Supervisor = jobs.OwnerIdentity{PID: probe.PID, BootID: probe.BootID, StartTicks: probe.StartTicks}
				r, err = e.save(r, n)
				if err != nil {
					t.Fatal(err)
				}
			}
			n := r
			n.LaunchAt = time.Now().Add(-time.Minute)
			r, err = e.save(r, n)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Close(time.Second); err != nil {
				t.Fatal(err)
			}
			cold, err := jobs.New(m.StateDir(), m.Policy())
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Close(time.Second)
			resumed := New(cold)
			cold.SetLeaseGuard(resumed.Guard)
			got := finish(t, resumed, r.ID)
			if got.State != "rolled_back" {
				t.Fatalf("missing receipt did not recover: %+v", got)
			}
			b, err := os.ReadFile(filepath.Join(root, "events"))
			if err != nil {
				t.Fatal(err)
			}
			want := "rollback\nfinalizer\n"
			if launched {
				want = "inspect\n" + want
			}
			if string(b) != want {
				t.Fatalf("replayed unknown stage: %q", b)
			}
		})
	}
}

func TestDamagedGitDoesNotEraseWriterFence(t *testing.T) {
	e, _, root, sha := canary(t, "")
	if _, err := e.Start(root, "release", "damaged-git", sha, "P7"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, ".git.hidden")); err != nil {
		t.Fatal(err)
	}
	if err := e.Guard(root, ""); err == nil {
		t.Fatal("damaged Git erased ownership")
	}
}

func TestProfileDriftAndOtherWriterAreRejected(t *testing.T) {
	e, _, root, sha := canary(t, "")
	r, err := e.Start(root, "release", "profile-drift", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Guard(root, ""); err == nil {
		t.Fatal("other writer admitted")
	}
	path := filepath.Join(root, ".mcpshka/profile.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "'inspect", "'changed", 1))
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	held, err := e.Tick(r.ID)
	if err != nil || held.State != "hold" {
		t.Fatalf("profile drift: %+v %v", held, err)
	}
}

func TestLostOrSubstitutedWorkflowClaimCannotExecuteOrResume(t *testing.T) {
	for _, substitution := range []string{"missing", "foreign"} {
		t.Run(substitution, func(t *testing.T) {
			e, _, root, sha := canary(t, "")
			r, err := e.Start(root, "release", "orphan-canary", sha, "P7")
			if err != nil {
				t.Fatal(err)
			}
			key := claimKey(root)
			owner, err := e.db.Get(key)
			if err != nil {
				t.Fatal(err)
			}
			if substitution == "missing" {
				ok, err := e.db.Delete(key, owner)
				if err != nil || !ok {
					t.Fatalf("delete claim: %v %v", ok, err)
				}
			} else {
				ok, err := e.db.Replace(key, owner, []byte("foreign-run"))
				if err != nil || !ok {
					t.Fatalf("replace claim: %v %v", ok, err)
				}
			}
			if _, err := e.Tick(r.ID); err == nil {
				t.Fatal("unowned workflow stage was executed")
			}
			if _, err := e.Start(root, "release", r.ID, sha, "P7"); err == nil {
				t.Fatal("idempotent start accepted stale ownership")
			}
			// A held workflow cannot be revived without its original writer claim.
			held := r
			held.State = "hold"
			if _, err := e.save(r, held); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Resume(r.ID); err == nil {
				t.Fatal("orphaned workflow resumed without original owner")
			}
			if _, err := os.Stat(filepath.Join(root, "events")); !os.IsNotExist(err) {
				t.Fatalf("orphaned workflow wrote to source: %v", err)
			}
		})
	}
}

func TestMaintenanceCursorGenerationAndLegacyMigration(t *testing.T) {
	key := "pipelines/v1/claims/first"
	legacy := []byte(key)
	name, generation, err := decodeMaintenanceCursor(legacy)
	if err != nil || name != key || generation != 0 {
		t.Fatalf("legacy claim rejected: %q %d %v", name, generation, err)
	}
	first, err := nextMaintenanceCursor(legacy, key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := nextMaintenanceCursor(first, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) {
		t.Fatal("single-claim pass reused the same CAS value")
	}
	got, gen, err := decodeMaintenanceCursor(second)
	if err != nil || got != key || gen != 2 {
		t.Fatalf("cursor generation missing: %q %d %v", got, gen, err)
	}
	for _, bad := range [][]byte{
		[]byte("unrecognized"),
		[]byte(maintenanceCursorPrefix + "garbage"),
		[]byte(maintenanceCursorPrefix + "0000000000000000:" + key),
		[]byte(maintenanceCursorPrefix + "ffffffffffffffff:" + key),
		[]byte("pipelines/v1/claims/bad path"),
	} {
		if _, err := nextMaintenanceCursor(bad, key); err == nil {
			t.Fatalf("unsafe cursor admitted: %q", bad)
		}
	}
}

func TestMaintenanceCursorRequiresExclusiveCAS(t *testing.T) {
	e, _, _, _ := canary(t, "")
	const key = "pipelines/v1/maintenance_cursor"
	first := []byte("pipelines/v1/claims/first")
	second := []byte("pipelines/v1/claims/second")
	third := []byte("pipelines/v1/claims/third")
	if err := e.advanceMaintenanceCursor(key, nil, first); err != nil {
		t.Fatal(err)
	}
	// A racing initial writer must not treat the lost PutIfAbsent as success.
	if err := e.advanceMaintenanceCursor(key, nil, second); err == nil {
		t.Fatal("concurrent first maintenance writer was admitted")
	}
	// A stale generation must not progress even when the key still exists.
	if err := e.advanceMaintenanceCursor(key, []byte("wrong-generation"), second); err == nil {
		t.Fatal("stale maintenance cursor was admitted")
	}
	got, err := e.db.Get(key)
	if err != nil || string(got) != string(first) {
		t.Fatalf("failed CAS changed cursor: %q %v", got, err)
	}
	if err := e.advanceMaintenanceCursor(key, first, second); err != nil {
		t.Fatal(err)
	}
	if err := e.advanceMaintenanceCursor(key, first, third); err == nil {
		t.Fatal("replayed writer advanced over a different successful writer")
	}
	got, err = e.db.Get(key)
	if err != nil || string(got) != string(second) {
		t.Fatalf("replayed cursor displaced winner: %q %v", got, err)
	}
}

func TestHeldClaimsCannotStarveLaterRun(t *testing.T) {
	e, _, root, sha := canary(t, "")
	seed, err := e.Start(root, "release", "fair-canary", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("held-%d", i)
		r := seed
		r.ID, r.State = id, "hold"
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.db.PutIfAbsent(runKey(id), raw); err != nil {
			t.Fatal(err)
		}
		if _, err = e.db.PutIfAbsent(fmt.Sprintf("pipelines/v1/claims/%02d", i), []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	r, err := e.Start(root, "release", "fair-canary", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Maintain(); err != nil {
		t.Fatal(err)
	}
	if err = e.Maintain(); err != nil {
		t.Fatal(err)
	}
	r, err = e.Get(r.ID)
	if err != nil || r.Index == 0 && !r.LaunchClaimed {
		t.Fatalf("run starved: state=%s index=%d reason=%s %v", r.State, r.Index, r.Reason, err)
	}
}

func TestCorruptedDurableSnapshotNeverExecutesOrDropsClaim(t *testing.T) {
	cases := []struct {
		name   string
		damage func(*Run)
	}{
		{"negative index", func(r *Run) { r.Index = -1 }},
		{"index past plans", func(r *Run) { r.Index = 12 }},
		{"unproven rollback", func(r *Run) { r.Index = 10 }},
		{"unproven finalizer", func(r *Run) { r.Index = 11 }},
		{"skipped prerequisite", func(r *Run) { r.Index = 1 }},
		{"substituted stage receipt", func(r *Run) {
			r.LaunchClaimed = true
			r.JobID = "workflow-" + strings.Repeat("a", 32)
			r.LaunchAt = time.Now()
		}},
		{"missing plans", func(r *Run) { r.Plans = nil }},
		{"record identity", func(r *Run) { r.ID = "other-run" }},
		{"unknown state", func(r *Run) { r.State = "unexpected" }},
		{"changed plan", func(r *Run) { r.Plans[0].Args = []string{"-c", "raise SystemExit(0)"} }},
		{"wrong plan root", func(r *Run) { r.Plans[0].Root = filepath.Dir(r.Root) }},
		{"unbound launch", func(r *Run) { r.LaunchClaimed = true; r.JobID = "unrelated-job" }},
		{"false completion", func(r *Run) { r.State = "done" }},
		{"missing completion receipt", func(r *Run) { r.State = "done"; r.Index = 9 }},
		{"missing recovery receipt", func(r *Run) { r.State = "rolled_back"; r.Index = 11 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _, root, sha := canary(t, "")
			r, err := e.Start(root, "release", "corrupt-run", sha, "P7")
			if err != nil {
				t.Fatal(err)
			}
			before, err := e.db.Get(runKey(r.ID))
			if err != nil {
				t.Fatal(err)
			}
			tc.damage(&r)
			after, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := e.db.Replace(runKey("corrupt-run"), before, after); err != nil || !ok {
				t.Fatalf("damage snapshot: %v %v", ok, err)
			}
			if _, err = e.Tick("corrupt-run"); err == nil {
				t.Error("corrupted snapshot admitted a transition")
			}
			claim, err := e.db.Get(claimKey(root))
			if err != nil || string(claim) != "corrupt-run" {
				t.Fatalf("corrupt snapshot released writer fence: %s %v", claim, err)
			}
			if _, err = os.Stat(filepath.Join(root, "events")); !os.IsNotExist(err) {
				t.Fatalf("corrupt snapshot executed a stage: %v", err)
			}
		})
	}
}

func TestCompletedTickSurvivesReceiptReaping(t *testing.T) {
	e, m, root, sha := canary(t, "")
	r, err := e.Start(root, "release", "reaped-completion", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	done := finish(t, e, r.ID)
	if done.State != "done" {
		t.Fatalf("not done: %+v", done)
	}
	count, err := m.Reap(0, time.Now().Add(time.Hour))
	if err != nil || count == 0 {
		t.Fatalf("reap: %d %v", count, err)
	}
	got, err := e.Tick(r.ID)
	if err != nil || got.State != "done" {
		t.Fatalf("completed tick: %+v %v", got, err)
	}
}

func TestMissingReceiptCauseCannotOverrideSuccessfulReceipt(t *testing.T) {
	e, m, root, sha := canary(t, "")
	r, err := e.Start(root, "release", "restored-success", sha, "P7")
	if err != nil {
		t.Fatal(err)
	}
	r, err = e.Tick(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		job, err := m.Get(r.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == jobs.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stage did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	n := r
	n.Recovery = &RecoveryCause{Index: r.Index, JobID: r.JobID, Supervisor: r.Supervisor, MissingReceipt: true}
	n.Index, n.State, n.JobID, n.LaunchClaimed = 10, "rollback", "", false
	n, err = e.save(r, n)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Tick(n.ID)
	if err != nil || got.State != "hold" || got.LaunchClaimed {
		t.Fatalf("successful receipt permitted recovery: state=%s launch=%v err=%v", got.State, got.LaunchClaimed, err)
	}
	events, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil || string(events) != "inspect\n" {
		t.Fatalf("unexpected mutation: %q %v", events, err)
	}
	claim, err := e.db.Get(claimKey(root))
	if err != nil || string(claim) != n.ID {
		t.Fatalf("claim lost: %q %v", claim, err)
	}
}

func TestOrphanSameIdentityNeverRecreatesWorkflow(t *testing.T) {
	e, _, root, sha := canary(t, "")
	if ok, err := e.db.PutIfAbsent(claimKey(root), []byte("orphan-run")); err != nil || !ok {
		t.Fatalf("fixture: %v %v", ok, err)
	}
	if _, err := e.Start(root, "release", "orphan-run", sha, "P7"); err == nil {
		t.Fatal("orphan claim authorized a replacement workflow")
	}
	if _, err := e.Get("orphan-run"); !os.IsNotExist(err) {
		t.Fatalf("orphan created a workflow: %v", err)
	}
}
