package offbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/jobs"
	"mcpshka/internal/runnerhub"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testDriver(t *testing.T) (*Driver, *clock, runnerhub.Job) {
	c := &clock{time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)}
	h := runnerhub.New(t.TempDir())
	h.Now = c.now
	j, err := runnerhub.Plan("/root/repos/demo", strings.Repeat("a", 40), "go-test", []string{"./internal/x"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	j.ID, j.Workdir = runnerhub.NewJobID(), "/root/repos/wt/demo-x"
	if j, err = h.Enqueue(j); err != nil {
		t.Fatal(err)
	}
	d := New(h, nil)
	d.Now = c.now
	return d, c, j
}

func localStub(calls *int) Local {
	return func() (string, error) { *calls++; return "h96-job", nil }
}

// Nothing off-box can serve: H96 takes the test at once, as before.
func TestNoRunnerNoWakerFallsBackAtOnce(t *testing.T) {
	d, _, j := testDriver(t)
	calls := 0
	if !d.step(context.Background(), j.ID, localStub(&calls)) || calls != 1 {
		t.Fatal("must fall back to H96 at once", calls)
	}
	got, _ := d.Hub.Job(j.ID)
	if got.State != runnerhub.NoRunner || got.Fallback != "h96-job" {
		t.Fatalf("%+v", got)
	}
}

// Sites are woken one by one, a refusal moves on at once, H96 is last.
func TestWakesSitesInTurnThenH96(t *testing.T) {
	t.Setenv("MCPSHKA_GIGAIDE_VCPU_HOURS_REMAINING", "20")
	d, c, j := testDriver(t)
	var woke []string
	d.Wake = func(_ context.Context, site string) error {
		woke = append(woke, site)
		if site == "gitverse" {
			return errors.New("dispatch refused")
		}
		return nil
	}
	calls := 0
	ctx := context.Background()
	d.step(ctx, j.ID, localStub(&calls)) // github
	c.t = c.t.Add(time.Minute)
	d.step(ctx, j.ID, localStub(&calls)) // github still has time
	c.t = c.t.Add(d.WakeEvery)
	d.step(ctx, j.ID, localStub(&calls)) // gitverse refuses
	d.step(ctx, j.ID, localStub(&calls)) // moshub at once
	if strings.Join(woke, ",") != "github,gitverse,moshub" || calls != 0 {
		t.Fatal(woke, calls)
	}
	c.t = c.t.Add(d.WakeEvery)
	d.step(ctx, j.ID, localStub(&calls)) // gigaide
	c.t = c.t.Add(d.WakeEvery)
	if !d.step(ctx, j.ID, localStub(&calls)) || calls != 1 {
		t.Fatal("after every site H96 must run it", woke, calls)
	}
	// The refused site cools down; the balancer starts the next job elsewhere.
	j2, _ := runnerhub.Plan("/root/repos/demo", strings.Repeat("b", 40), "go-test", nil, 0)
	j2.ID = runnerhub.NewJobID()
	j2, _ = d.Hub.Enqueue(j2)
	if site := d.pick(j2, c.t); site != "github" {
		t.Fatalf("least recently woken first, refused one cooling: %s", site)
	}
}

// A job a runner leased stays off-box: no wake, no fallback.
func TestLeasedJobStaysOffBox(t *testing.T) {
	d, _, j := testDriver(t)
	tok, _, _ := d.Hub.MintEphemeral("github")
	rid, _, err := d.Hub.Register(tok, "ci-github-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := d.Hub.Poll(rid, map[string]string{"go": "1"}, 0); err != nil || got == nil {
		t.Fatal("poll", err)
	}
	d.Wake = func(context.Context, string) error { t.Fatal("woke while leased"); return nil }
	calls := 0
	if d.step(context.Background(), j.ID, localStub(&calls)) || calls != 0 {
		t.Fatal("leased job must keep the driver waiting")
	}
	s, _ := View{Hub: d.Hub}.Get(j.ID)
	if s.State != jobs.Running || !strings.Contains(s.Queue.Reason, "off-box on github") {
		t.Fatalf("%+v %+v", s, s.Queue)
	}
}

type localJobs struct{}

func (localJobs) Get(id string) (jobs.Snapshot, error) {
	return jobs.Snapshot{ID: id, State: jobs.Done}, nil
}
func (localJobs) Read(id, stream string, c int64, l int) (jobs.Output, error) {
	return jobs.Output{JobID: id, Text: "ok h96"}, nil
}

// After a fallback the runner job id shows the H96 job.
func TestViewFollowsFallback(t *testing.T) {
	d, _, j := testDriver(t)
	calls := 0
	d.step(context.Background(), j.ID, localStub(&calls))
	v := View{Hub: d.Hub, Local: localJobs{}}
	if s, _ := v.Get(j.ID); s.ID != "h96-job" || s.State != jobs.Done {
		t.Fatalf("%+v", s)
	}
	if o, _ := v.Read(j.ID, "", 0, 0); o.Text != "ok h96" {
		t.Fatalf("%+v", o)
	}
}

// The snapshot pins uncommitted changes without moving the branch or index.
func TestSnapshotCommitPinsWorkingTree(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	run("add", "a.go")
	run("commit", "-qm", "one")
	head := run("rev-parse", "HEAD")
	ctx := context.Background()
	if c, err := SnapshotCommit(ctx, dir); err != nil || c != head {
		t.Fatal("clean tree must pin HEAD", c, err)
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n// changed\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644)
	c, err := SnapshotCommit(ctx, dir)
	if err != nil || c == head {
		t.Fatal(c, err)
	}
	if got := run("show", c+":a.go"); !strings.Contains(got, "changed") {
		t.Fatal("snapshot lacks the change", got)
	}
	run("cat-file", "-e", c+":b.go")
	run("diff", "--cached", "--quiet") // index untouched: nothing staged
	if run("rev-parse", "HEAD") != head {
		t.Fatal("branch moved")
	}
}

type wakeJobs struct{ out string }

func (w wakeJobs) Get(id string) (jobs.Snapshot, error) {
	return jobs.Snapshot{ID: id, State: jobs.Done}, nil
}
func (w wakeJobs) Read(id, stream string, c int64, l int) (jobs.Output, error) {
	return jobs.Output{JobID: id, Text: w.out}, nil
}

// A wake counts only when the forge job reports accepted.
func TestJobWakerReadsWakeReport(t *testing.T) {
	start := func(string) (string, error) { return "j1", nil }
	ok := JobWaker(start, wakeJobs{`{"schema":"mcpshka.runner-wake/v1","site":"github","state":"accepted"}`})
	if err := ok(context.Background(), "github"); err != nil {
		t.Fatal(err)
	}
	refused := JobWaker(start, wakeJobs{`{"state":"refused","reason":"LOGIN_NEEDED"}`})
	if err := refused(context.Background(), "gigaide"); err == nil || !strings.Contains(err.Error(), "LOGIN_NEEDED") {
		t.Fatal(err)
	}
}
