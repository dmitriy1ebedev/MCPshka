package runnerhub

import (
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testHub(t *testing.T) (*Hub, *clock) {
	c := &clock{time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	h := New(t.TempDir())
	h.Now = c.now
	return h, c
}

func register(t *testing.T, h *Hub, name string) (string, string) {
	tok, _, err := h.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	id, key, err := h.Register(tok, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Register(tok, name+"2"); err == nil {
		t.Fatal("token reused")
	}
	return id, key
}

func job(t *testing.T, h *Hub) Job {
	j, err := Plan("/root/repos/demo", strings.Repeat("a", 40), "go-test", []string{"./internal/x"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	j.ID = NewJobID()
	j, err = h.Enqueue(j)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// The GitHub-Actions-style cycle: one-time token → key → lease → heartbeat →
// result; a sleeping runner loses the lease and the job returns to the queue.
func TestRunnerLeaseCycle(t *testing.T) {
	h, c := testHub(t)
	id, key := register(t, h, "gigaide")
	if r, err := h.Auth(key); err != nil || r.ID != id {
		t.Fatalf("auth: %v", err)
	}
	if _, err := h.Auth("mrk_forged"); err == nil {
		t.Fatal("forged key accepted")
	}
	j := job(t, h)
	if got, _ := h.Poll(id, map[string]string{"os": "linux"}, 0); got != nil {
		t.Fatal("job given to a runner without go")
	}
	got, err := h.Poll(id, map[string]string{"go": "auto"}, 0)
	if err != nil || got == nil || got.ID != j.ID || got.Argv[len(got.Argv)-1] != "./internal/x" {
		t.Fatalf("poll: %+v %v", got, err)
	}
	if again, _ := h.Poll(id, map[string]string{"go": "auto"}, 0); again != nil {
		t.Fatal("second job while busy")
	}
	c.t = c.t.Add(60 * time.Second)
	if cancel, _ := h.Heartbeat(id, j.ID); cancel {
		t.Fatal("heartbeat cancelled a live lease")
	}
	c.t = c.t.Add(LeaseTTL + time.Second) // runner fell asleep
	if s, _ := h.Job(j.ID); s.State != Queued || s.Runner != "" {
		t.Fatalf("lost lease not requeued: %+v", s)
	}
	if cancel, _ := h.Heartbeat(id, j.ID); !cancel {
		t.Fatal("stale runner not told to stop")
	}
	if err := h.Result(id, j.ID, 0, "ok", ""); err == nil {
		t.Fatal("stale runner result accepted")
	}
	got, _ = h.Poll(id, map[string]string{"go": "go1.27"}, 0)
	if got == nil || got.Attempts != 2 {
		t.Fatalf("requeued job not leased again: %+v", got)
	}
	if err := h.Result(id, j.ID, 1, "--- FAIL: TestX", ""); err != nil {
		t.Fatal(err)
	}
	s, _ := h.Job(j.ID)
	if s.State != Failed || s.OutputSHA == "" || s.Trust != "UNTRUSTED" {
		t.Fatalf("result: %+v", s)
	}
	rs, _, _ := h.Snapshot()
	if len(rs) != 1 || rs[0].KeyHash != "" || rs[0].Job != "" {
		t.Fatalf("snapshot leaks or keeps job: %+v", rs)
	}
}

func TestRunnerGivesUpAndNoRunner(t *testing.T) {
	h, c := testHub(t)
	id, _ := register(t, h, "lap")
	j := job(t, h)
	for i := 0; i < MaxAttempts; i++ {
		if got, _ := h.Poll(id, map[string]string{"go": "auto"}, 0); got == nil {
			t.Fatalf("attempt %d not leased", i+1)
		}
		c.t = c.t.Add(LeaseTTL + time.Second)
	}
	if s, _ := h.Job(j.ID); s.State != Failed {
		t.Fatalf("after %d lost leases: %+v", MaxAttempts, s)
	}
	idle := job(t, h)
	c.t = c.t.Add(DefaultWait + time.Second)
	if s, _ := h.Job(idle.ID); s.State != NoRunner || !strings.Contains(s.Error, "project_test") {
		t.Fatalf("no-runner fallback: %+v", s)
	}
}

func TestRunnerDisableRequeuesAndLongPollWakes(t *testing.T) {
	h, _ := testHub(t)
	id, key := register(t, h, "cs")
	h.Now = time.Now
	done := make(chan *Job, 1)
	go func() { j, _ := h.Poll(id, map[string]string{"go": "auto"}, 5*time.Second); done <- j }()
	time.Sleep(50 * time.Millisecond)
	j := job(t, h)
	select {
	case got := <-done:
		if got == nil || got.ID != j.ID {
			t.Fatalf("long poll: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("enqueue did not wake the long poll")
	}
	if err := h.Disable(id); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.Job(j.ID); s.State != Queued {
		t.Fatalf("disabled runner kept its job: %+v", s)
	}
	if _, err := h.Auth(key); err == nil {
		t.Fatal("disabled runner still authenticates")
	}
}

func TestRunnerPlanRefusesFreeForm(t *testing.T) {
	sha := strings.Repeat("b", 40)
	for _, bad := range []struct {
		commit, profile string
		pkgs            []string
	}{
		{"HEAD", "go-test", nil},
		{sha, "bash", nil},
		{sha, "go-test", []string{"./../etc"}},
		{sha, "go-test", []string{"-exec=sh"}},
		{sha, "go-test", []string{"./x;rm"}},
	} {
		if _, err := Plan("/root/repos/demo", bad.commit, bad.profile, bad.pkgs, 0); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	j, err := Plan("/root/repos/demo", sha, "go-vet", nil, 99999)
	if err != nil || strings.Join(j.Argv, " ") != "go vet ./..." || j.Timeout != 3*3600 {
		t.Fatalf("plan: %+v %v", j, err)
	}
	if len(Agent) < 1000 || !strings.Contains(string(Agent), "/runner/") {
		t.Fatal("agent script not embedded")
	}
}

func TestNeedsMetRequiresEveryCap(t *testing.T) {
	caps := map[string]string{"go": "go1.26", "sqlite3": "1"}
	if !NeedsMet("", caps) || !NeedsMet("go sqlite3", caps) || NeedsMet("go sqlite3 pyyaml", caps) {
		t.Fatal("needs must all be present")
	}
}

func TestSetupScriptCarriesOrigin(t *testing.T) {
	s := string(Setup("https://h.example"))
	if strings.Contains(s, "@ORIGIN@") || !strings.Contains(s, "U='https://h.example'") || !strings.Contains(s, "--conf") {
		t.Fatalf("setup script not rendered:\n%s", s)
	}
}

func TestPassedOnWholeModuleGoTest(t *testing.T) {
	h := New(t.TempDir())
	c := strings.Repeat("a", 40)
	for _, pk := range [][]string{{"./x"}, nil} {
		j, err := Plan("/p", c, "go-test", pk, 0)
		if err != nil {
			t.Fatal(err)
		}
		j.ID = NewJobID()
		if _, err := h.Enqueue(j); err != nil {
			t.Fatal(err)
		}
	}
	_ = h.update(func(s *state) error {
		for i := range s.Jobs {
			s.Jobs[i].State = Done
		}
		return nil
	})
	if _, ok := h.PassedOn("/p", c); !ok {
		t.Fatal("whole-module pass not seen")
	}
	if _, ok := h.PassedOn("/p", strings.Repeat("b", 40)); ok {
		t.Fatal("other commit counted")
	}
}

func TestFitNamesMissingCaps(t *testing.T) {
	h := New(t.TempDir())
	if got := h.Fit("go"); !strings.Contains(got, "no runner") {
		t.Fatalf("empty hub: %q", got)
	}
	tok, _, err := h.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := h.Register(tok, "ide")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Poll(id, map[string]string{"go": "go1.26"}, 0); err != nil {
		t.Fatal(err)
	}
	if got := h.Fit("go sqlite3 pyyaml"); !strings.Contains(got, "sqlite3, pyyaml") {
		t.Fatalf("missing caps: %q", got)
	}
	if got := h.Fit("go"); got != "" {
		t.Fatalf("fitting runner: %q", got)
	}
}

// A wake token registers an ephemeral runner of its site, once; the runner
// disappears after it goes quiet, an owner runner stays.
func TestEphemeralRunnerOfWokenSite(t *testing.T) {
	h, c := testHub(t)
	if _, _, err := h.MintEphemeral("GitHub!"); err == nil {
		t.Fatal("bad site name accepted")
	}
	tok, exp, err := h.MintEphemeral("github")
	if err != nil || exp.Sub(c.t) != EphemeralTTL {
		t.Fatal(err, exp)
	}
	id, _, err := h.Register(tok, "ci-github-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Register(tok, "again"); err == nil {
		t.Fatal("wake token reused")
	}
	owner, _ := register(t, h, "laptop")
	rs, _, _ := h.Snapshot()
	if len(rs) != 2 || rs[0].ID != id || rs[0].Site != "github" || rs[1].Site != "" {
		t.Fatalf("%+v", rs)
	}
	c.t = c.t.Add(EphemeralQuiet + time.Minute)
	rs, _, _ = h.Snapshot()
	if len(rs) != 1 || rs[0].ID != owner {
		t.Fatalf("quiet ephemeral runner must go, owner runner stay: %+v", rs)
	}
}
