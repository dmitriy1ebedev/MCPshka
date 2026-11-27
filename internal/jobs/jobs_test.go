package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/policy"
)

func testManager(t *testing.T) (*Manager, string) {
	t.Helper()
	work := t.TempDir()
	state := t.TempDir()
	p := &policy.Policy{
		AllowedWorkdirs: []string{work},
		DeniedCommands:  map[string]struct{}{},
	}
	m, err := New(state, p)
	if err != nil {
		t.Fatal(err)
	}
	m.heavyQueue = heavyqueue.Queue{Dir: t.TempDir(), Poll: time.Millisecond, Ready: func() (bool, string) { return true, "" }}
	m.loadAvg = func() float64 { return 0 } // budgets must not depend on the test host's load
	return m, work
}

func waitForTerminal(t *testing.T, m *Manager, id string) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if s.State != Running && s.State != Queued && s.State != Cancelling {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not reach terminal state")
	return Snapshot{}
}

func TestExplicitJobEnvironment(t *testing.T) {
	m, work := testManager(t)
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env missing")
	}
	job, err := m.StartWithOptions(env, nil, work, StartOptions{Env: map[string]string{"MSPSHKA_TEST": "enabled"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := waitForTerminal(t, m, job.ID); s.State != Done {
		t.Fatalf("job failed: %+v", s)
	}
	out, err := m.Read(job.ID, "stdout", 0, MaxReadLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "MSPSHKA_TEST=enabled\n") {
		t.Fatalf("missing explicit env: %q", out.Text)
	}
	if _, err := m.StartWithOptions(env, nil, work, StartOptions{Env: map[string]string{"PATH": "/unsafe"}}); err == nil {
		t.Fatal("reserved baseline override accepted")
	}
}

func TestJobOutputCursor(t *testing.T) {
	m, work := testManager(t)
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	j, err := m.Start(printf, []string{"hello"}, work)
	if err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, j.ID)
	if s.State != Done || s.ExitCode == nil || *s.ExitCode != 0 {
		t.Fatalf("unexpected final state: %+v", s)
	}

	first, err := m.Read(j.ID, "stdout", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "he" || first.NextCursor != 2 || !first.Truncated {
		t.Fatalf("unexpected first read: %+v", first)
	}

	second, err := m.Read(j.ID, "stdout", first.NextCursor, 16)
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "llo" || second.NextCursor != 5 || second.Truncated {
		t.Fatalf("unexpected second read: %+v", second)
	}
}

func TestOutputHardCap(t *testing.T) {
	m, work := testManager(t)
	seq, err := exec.LookPath("seq")
	if err != nil {
		t.Skip("seq not found")
	}
	j, err := m.Start(seq, []string{"1", "30000"}, work)
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminal(t, m, j.ID)

	out, err := m.Read(j.ID, "stdout", 0, MaxReadLimit*4)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Text) > MaxReadLimit {
		t.Fatalf("read exceeded hard cap: %d", len(out.Text))
	}
	if !out.Truncated {
		t.Fatalf("expected large output to be truncated: total=%d", out.TotalBytes)
	}
}

func TestCancelKillsJob(t *testing.T) {
	m, work := testManager(t)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	j, err := m.Start(sleep, []string{"10"}, work)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, j.ID)
	if s.State != Cancelled {
		t.Fatalf("want cancelled, got %+v", s)
	}
}

func TestDeniedCommand(t *testing.T) {
	work := t.TempDir()
	p := &policy.Policy{
		AllowedWorkdirs: []string{work},
		DeniedCommands:  map[string]struct{}{"sleep": {}},
	}
	m, err := New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	if _, err := m.Start(sleep, []string{"1"}, work); err == nil {
		t.Fatal("expected denied command error")
	}
}

func TestJobMetadataSurvivesManagerRestart(t *testing.T) {
	work := t.TempDir()
	state := t.TempDir()
	p := &policy.Policy{AllowedWorkdirs: []string{work}, DeniedCommands: map[string]struct{}{}}
	m1, err := New(state, p)
	if err != nil {
		t.Fatal(err)
	}
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	j, err := m1.Start(printf, []string{"persisted"}, work)
	if err != nil {
		t.Fatal(err)
	}
	want := waitForTerminal(t, m1, j.ID)

	m2, err := New(state, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m2.Get(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != want.State || got.Command != want.Command || got.Workdir != want.Workdir {
		t.Fatalf("restored snapshot mismatch: got=%+v want=%+v", got, want)
	}
	out, err := m2.Read(j.ID, "stdout", 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "persisted" {
		t.Fatalf("restored output = %q", out.Text)
	}
}

func TestLoadPreservesInterruptedJobUnknown(t *testing.T) {
	state := t.TempDir()
	work := t.TempDir()
	id := "interrupted"
	jobDir := filepath.Join(state, id)
	if err := os.MkdirAll(jobDir, 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute).UTC()
	s := Snapshot{
		ID: id, Command: "sleep", Args: []string{"100"}, Workdir: work,
		State: Running, PID: 999999, StartedAt: started,
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, metadataName), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "stdout.log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "stderr.log"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	p := &policy.Policy{AllowedWorkdirs: []string{work}, DeniedCommands: map[string]struct{}{}}
	m, err := New(state, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Unknown || !got.FinishedAt.IsZero() {
		t.Fatalf("interrupted job must retain unknown outcome: %+v", got)
	}
	if !strings.Contains(got.Error, "restarted") {
		t.Fatalf("unexpected recovery error: %q", got.Error)
	}
	if _, err := m.Cancel(id); err == nil || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("unknown ownership cancellation accepted: %v", err)
	}
	if removed, err := m.Reap(0, time.Now().Add(time.Hour)); err != nil || removed != 0 {
		t.Fatalf("unknown job evidence deleted: removed=%d err=%v", removed, err)
	}
	m.maxConcurrent = 1
	if _, err := m.Start("printf", []string{"x"}, work); err == nil || !strings.Contains(err.Error(), "concurrent job limit") {
		t.Fatalf("unknown job did not reserve capacity: %v", err)
	}
}

func TestReapRemovesExpiredTerminalJobs(t *testing.T) {
	m, work := testManager(t)
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	j, err := m.Start(printf, []string{"old"}, work)
	if err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, j.ID)
	removed, err := m.Reap(0, s.FinishedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d want=1", removed)
	}
	if _, err := m.Get(j.ID); err == nil {
		t.Fatal("reaped job still present")
	}
	if _, err := os.Stat(filepath.Join(m.root, j.ID)); !os.IsNotExist(err) {
		t.Fatalf("job directory still exists: %v", err)
	}
}

func TestQuotaWriterCapsStoredBytes(t *testing.T) {
	var dst bytes.Buffer
	q := &outputQuota{limit: 5}
	w := &quotaWriter{dst: &dst, quota: q}
	n, err := w.Write([]byte("abcdefgh"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Fatalf("reported write=%d want=8", n)
	}
	if got := dst.String(); got != "abcde" {
		t.Fatalf("stored=%q", got)
	}
	stored, dropped := q.stats()
	if stored != 5 || dropped != 3 {
		t.Fatalf("stored=%d dropped=%d", stored, dropped)
	}
}

func TestJobOutputDiskQuota(t *testing.T) {
	m, work := testManager(t)
	m.defaultOutputCap = 1024
	seq, err := exec.LookPath("seq")
	if err != nil {
		t.Skip("seq not found")
	}
	j, err := m.Start(seq, []string{"1", "10000"}, work)
	if err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, j.ID)
	if s.State != Done {
		t.Fatalf("unexpected state: %+v", s)
	}
	if s.OutputStoredBytes > 1024 || s.OutputDroppedBytes == 0 {
		t.Fatalf("quota not enforced: %+v", s)
	}
	info, err := os.Stat(filepath.Join(m.root, j.ID, "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1024 {
		t.Fatalf("stdout.log exceeded cap: %d", info.Size())
	}
}

func TestJobWallClockTimeout(t *testing.T) {
	m, work := testManager(t)
	m.defaultTimeout = 50 * time.Millisecond
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	j, err := m.Start(sleep, []string{"5"}, work)
	if err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, j.ID)
	if s.State != TimedOut {
		t.Fatalf("want timed_out, got %+v", s)
	}
	if s.TimeoutSeconds != 0 {
		t.Fatalf("sub-second test timeout should persist as 0 seconds, got %d", s.TimeoutSeconds)
	}
}

func TestConcurrentJobLimit(t *testing.T) {
	m, work := testManager(t)
	m.maxConcurrent = 1
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	first, err := m.Start(sleep, []string{"5"}, work)
	if err != nil {
		t.Fatal(err)
	}
	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	if _, err := m.Start(printf, []string{"second"}, work); err == nil {
		t.Fatal("expected concurrent job limit rejection")
	}
	if _, err := m.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	s := waitForTerminal(t, m, first.ID)
	if s.State != Cancelled {
		t.Fatalf("unexpected cancelled job state: %+v", s)
	}
}

func TestDefaultPolicyClassIsReturnedAndDestructiveRejectedBeforeStart(t *testing.T) {
	work := t.TempDir()
	p := policy.Default()
	p.AllowedWorkdirs = []string{work}
	m, err := New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}

	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	j, err := m.Start(printf, []string{"classified"}, work)
	if err != nil {
		t.Fatal(err)
	}
	if j.CommandClass != policy.ClassReadOnly {
		t.Fatalf("command_class=%q want=%q", j.CommandClass, policy.ClassReadOnly)
	}
	waitForTerminal(t, m, j.ID)

	_, err = m.Start("rm", []string{"-rf", filepath.Join(work, "never-created")}, work)
	if err == nil {
		t.Fatal("destructive command unexpectedly started")
	}
	if !strings.Contains(err.Error(), string(policy.ClassDestructive)) {
		t.Fatalf("rejection does not expose class: %v", err)
	}
}

func TestProjectRoleProfileEnforcedBeforeStart(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	open := filepath.Join(root, "open")
	for _, dir := range []string{locked, open} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	p := policy.Default()
	p.AllowedWorkdirs = []string{root}
	if err := p.SetProjectProfile(locked, policy.ProfileReader); err != nil {
		t.Fatal(err)
	}
	m, err := New(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true not found")
	}
	if _, err := m.Start(trueBin, nil, locked); err == nil {
		t.Fatal("reader project started mutating-class command")
	}
	job, err := m.Start(trueBin, nil, open)
	if err != nil {
		t.Fatal(err)
	}
	if s := waitForTerminal(t, m, job.ID); s.State != Done {
		t.Fatalf("operator project job failed: %+v", s)
	}
}

func TestSnapshotReportsQuietTimeAndOutputActivity(t *testing.T) {
	started := time.Now().UTC().Add(-3 * time.Second)
	j := &Job{ID: "quiet", State: Running, StartedAt: started, quota: &outputQuota{limit: 1024}}

	quiet := snapshot(j)
	if quiet.QuietSeconds < 2 || !quiet.LastOutputAt.IsZero() {
		t.Fatalf("quiet job observability mismatch: %+v", quiet)
	}

	var dst bytes.Buffer
	w := &quotaWriter{dst: &dst, quota: j.quota}
	if _, err := w.Write([]byte("progress")); err != nil {
		t.Fatal(err)
	}
	active := snapshot(j)
	if active.LastOutputAt.IsZero() {
		t.Fatalf("last_output_at was not recorded: %+v", active)
	}
	if active.QuietSeconds > 1 {
		t.Fatalf("recent output should reset quiet time: %+v", active)
	}
}

func TestCloseTerminatesRunningJobsAndRejectsNewStarts(t *testing.T) {
	m, work := testManager(t)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not found")
	}
	j, err := m.Start(sleep, []string{"30"}, work)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Close(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	s, err := m.Get(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != Cancelled {
		t.Fatalf("state=%q want=%q snapshot=%+v", s.State, Cancelled, s)
	}
	if !strings.Contains(s.Error, "shutting down") {
		t.Fatalf("shutdown reason missing: %+v", s)
	}
	if err := syscall.Kill(j.PID, 0); err == nil || err != syscall.ESRCH {
		t.Fatalf("process %d still exists after Close: %v", j.PID, err)
	}

	printf, err := exec.LookPath("printf")
	if err != nil {
		t.Skip("printf not found")
	}
	if _, err := m.Start(printf, []string{"should-not-start"}, work); err == nil ||
		!strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("new start after Close was not rejected: %v", err)
	}
}

func TestTerminateJobHardCleansProcessGroupAfterLeaderExit(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}
	cmd := exec.Command(sh, "-c", "trap 'exit 0' TERM; sleep 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	m, err := New(t.TempDir(), policy.Default())
	if err != nil {
		t.Fatal(err)
	}
	m.terminateJob(pid, "", nil)
	_ = cmd.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process group %d survived hard-cleanup grace period", pid)
}

func TestProjectActivityIsBoundedAndDoesNotExposeSecrets(t *testing.T) {
	m, work := testManager(t)
	m.jobs["b"] = &Job{ID: "b", Workdir: filepath.Join(work, "nested"), State: Unknown, Args: []string{"SECRET"}}
	m.jobs["a"] = &Job{ID: "a", Workdir: work, State: Failed, Error: "mcpshka restarted while job was active", FinishedAt: time.Now().Add(-time.Hour)}
	if n, err := m.Reap(0, time.Now()); err != nil || n != 0 {
		t.Fatalf("legacy unknown evidence was reaped: %d, %v", n, err)
	}
	m.jobs["neighbor"] = &Job{ID: "neighbor", Workdir: work + "-other", State: Unknown}
	m.jobs["done"] = &Job{ID: "done", Workdir: work, State: Done}
	got := m.ProjectActivity(work, 1)
	if got.Count != 2 || got.UnknownCount != 2 || !got.Truncated || len(got.Jobs) != 1 || got.Jobs[0].ID != "a" {
		t.Fatalf("unsafe activity summary: %+v", got)
	}
	b, err := json.Marshal(m.ProjectActivity(work, 64))
	if err != nil || strings.Contains(string(b), "SECRET") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestUnknownReconcileAndReceiptRemainAvailable(t *testing.T) {
	m, work := testManager(t)
	m.maxConcurrent = 1
	m.jobs["old"] = &Job{ID: "old", State: Unknown, Workdir: work, PID: 1, BootID: "previous-boot", StartTicks: 1}
	receipt, err := m.StoreReceipt("mcpshka", []string{"jobs-audit"}, work, "{}")
	if err != nil || receipt.State != Done {
		t.Fatalf("recovery receipt blocked: %v", err)
	}
	s, err := m.Reconcile("old")
	if err != nil || s.State != Unknown || !s.ResourcesReleased || s.ReconciledAt.IsZero() {
		t.Fatalf("unsafe reconcile: %+v %v", s, err)
	}
	if _, err := m.Start("printf", []string{"ok"}, work); err != nil {
		t.Fatalf("verified gone resources still block admission: %v", err)
	}
	m.jobs["legacy"] = &Job{ID: "legacy", State: Unknown, Workdir: work, PID: 1}
	if _, err := m.Reconcile("legacy"); err == nil {
		t.Fatal("legacy identity was guessed")
	}
	boot, start, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	m.jobs["live"] = &Job{ID: "live", State: Unknown, Workdir: work, PID: os.Getpid(), BootID: boot, StartTicks: start}
	if _, err := m.Reconcile("live"); err == nil {
		t.Fatal("live owner resources released")
	}
	summary := m.RegistrySummary(64)
	if summary.Count < 3 {
		t.Fatalf("root registry summary skipped active evidence: %+v", summary)
	}
}

func TestP6TunnelAndSessionLossKeepsDurableJob(t *testing.T) {
	// Test an isolated transport, not the production tunnel or network routes.
	for iteration := 0; iteration < 3; iteration++ {
		t.Run(fmt.Sprintf("transport-%d", iteration), func(t *testing.T) {
			m, work := testManager(t)
			left, right := net.Pipe()
			sleep, err := exec.LookPath("sleep")
			if err != nil {
				t.Skip("sleep not found")
			}
			job, err := m.StartWithOptions(sleep, []string{"0.25"}, work, StartOptions{Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			// Losing both the tunnel and the AI client is not a job-cancel request.
			_ = right.Close()
			_ = left.Close()
			done := waitForTerminal(t, m, job.ID)
			if done.State != Done || done.ExitCode == nil || *done.ExitCode != 0 {
				t.Fatalf("transport disconnect changed job outcome: %+v", done)
			}
			p := &policy.Policy{AllowedWorkdirs: []string{work}, DeniedCommands: map[string]struct{}{}}
			fresh, err := New(m.root, p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := fresh.Get(job.ID)
			if err != nil || got.State != Done || got.ExitCode == nil || *got.ExitCode != 0 {
				t.Fatalf("session reconnection lost job evidence: %+v %v", got, err)
			}
		})
	}
}

func TestP6ChaosChild(t *testing.T) {
	mode := os.Getenv("MCPSHKA_P6_CHAOS_MODE")
	if mode == "" {
		return
	}
	work := os.Getenv("MCPSHKA_P6_CHAOS_WORK")
	state := os.Getenv("MCPSHKA_P6_CHAOS_STATE")
	marker := os.Getenv("MCPSHKA_P6_CHAOS_MARKER")
	switch mode {
	case "worker":
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("effect\\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Second)
	case "supervisor":
		p := &policy.Policy{AllowedWorkdirs: []string{work}, DeniedCommands: map[string]struct{}{}}
		m, err := New(state, p)
		if err != nil {
			t.Fatal(err)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		j, err := m.StartWithOptions(exe, []string{"-test.run=^TestP6ChaosChild$"}, work, StartOptions{
			Timeout: 20 * time.Second,
			Env: map[string]string{
				"MCPSHKA_P6_CHAOS_MODE":   "worker",
				"MCPSHKA_P6_CHAOS_WORK":   work,
				"MCPSHKA_P6_CHAOS_STATE":  state,
				"MCPSHKA_P6_CHAOS_MARKER": marker,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("MCPSHKA_P6_CHAOS_READY"), payload, 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Second)
	default:
		t.Fatalf("unexpected chaos mode: %s", mode)
	}
}

// A Linux subreaper owns the isolated worker after the supervisor is killed.
// The test reaps every child, never touching the production daemon or tunnel.
func TestP6ChaosSupervisorCrashUnknownNoDuplicate(t *testing.T) {
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); errno != 0 {
		t.Skipf("subreaper unavailable: %v", errno)
	}
	defer syscall.RawSyscall6(syscall.SYS_PRCTL, 36, 0, 0, 0, 0, 0)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 3; iteration++ {
		t.Run(fmt.Sprintf("crash-%d", iteration), func(t *testing.T) {
			parent := t.TempDir()
			work := filepath.Join(parent, "work")
			state := filepath.Join(parent, "state", "jobs")
			if err := os.MkdirAll(work, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(parent, "effect.txt")
			ready := filepath.Join(parent, "ready.json")
			supervisor := exec.Command(exe, "-test.run=^TestP6ChaosChild$")
			supervisor.Env = append(os.Environ(),
				"MCPSHKA_P6_CHAOS_MODE=supervisor",
				"MCPSHKA_P6_CHAOS_WORK="+work,
				"MCPSHKA_P6_CHAOS_STATE="+state,
				"MCPSHKA_P6_CHAOS_MARKER="+marker,
				"MCPSHKA_P6_CHAOS_READY="+ready,
			)
			if err := supervisor.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = supervisor.Process.Kill()
				_ = supervisor.Wait()
			}()
			var started Snapshot
			deadline := time.Now().Add(6 * time.Second)
			for time.Now().Before(deadline) {
				b, e1 := os.ReadFile(ready)
				effect, e2 := os.ReadFile(marker)
				if e1 == nil && e2 == nil && string(effect) == "effect\\n" && json.Unmarshal(b, &started) == nil && started.PID > 0 {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if started.PID == 0 {
				t.Fatal("isolated worker did not persist its start before crash")
			}
			defer func() { _ = syscall.Kill(-started.PID, syscall.SIGKILL) }()
			if err := supervisor.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = supervisor.Wait()
			p := &policy.Policy{AllowedWorkdirs: []string{work}, DeniedCommands: map[string]struct{}{}}
			recovered, err := New(state, p)
			if err != nil {
				t.Fatal(err)
			}
			s, err := recovered.Get(started.ID)
			if err != nil || s.State != Unknown || s.ResourcesReleased {
				t.Fatalf("unexpected crash recovery state: %+v, %v", s, err)
			}
			if _, err := recovered.Reconcile(started.ID); err == nil {
				t.Fatal("live, PID-bound orphan falsely reconciled")
			}
			recovered.maxConcurrent = 1
			if _, err := recovered.Start("printf", []string{"duplicate"}, work); err == nil {
				t.Fatal("replayed an unknown mutation while worker was alive")
			}
			if err := syscall.Kill(-started.PID, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			var status syscall.WaitStatus
			if _, err := syscall.Wait4(started.PID, &status, 0, nil); err != nil {
				t.Fatalf("orphan worker was not safely reaped: %v", err)
			}
			got, err := recovered.Reconcile(started.ID)
			if err != nil || got.State != Unknown || !got.ResourcesReleased || got.ReconciledAt.IsZero() {
				t.Fatalf("resource reconciliation forged an outcome: %+v, %v", got, err)
			}
			contents, err := os.ReadFile(marker)
			if err != nil || string(contents) != "effect\\n" {
				t.Fatalf("effect not exactly once: %q, %v", contents, err)
			}
			third, err := New(state, p)
			if err != nil {
				t.Fatal(err)
			}
			final, err := third.Get(started.ID)
			if err != nil || final.State != Unknown || !final.ResourcesReleased {
				t.Fatalf("recovery not durable after second restart: %+v, %v", final, err)
			}
		})
	}
}

func TestReconcileUnknownRotatesAcrossHeldJobs(t *testing.T) {
	m, work := testManager(t)
	for i := 0; i < 33; i++ {
		id := fmt.Sprintf("hold-%02d", i)
		m.jobs[id] = &Job{ID: id, State: Unknown, Workdir: work}
	}
	m.jobs["z-recoverable"] = &Job{
		ID: "z-recoverable", State: Unknown, Workdir: work,
		PID: 1, BootID: "previous-boot", StartTicks: 1,
	}
	m.ReconcileUnknown(16)
	if m.reconcileCursor != "hold-15" {
		t.Fatalf("first page cursor = %q", m.reconcileCursor)
	}
	m.ReconcileUnknown(16)
	if m.reconcileCursor != "hold-31" {
		t.Fatalf("second page cursor = %q", m.reconcileCursor)
	}
	m.ReconcileUnknown(16)
	got, err := m.Get("z-recoverable")
	if err != nil || !got.ResourcesReleased || got.State != Unknown {
		t.Fatalf("recoverable job starved behind HOLD entries: %+v, %v", got, err)
	}
	for i := 0; i < 33; i++ {
		id := fmt.Sprintf("hold-%02d", i)
		if m.jobs[id].ResourcesReleased {
			t.Fatalf("unverified HOLD resource released: %s", id)
		}
	}
}

func TestJobPrivateTemporaryDirectory(t *testing.T) {
	m, work := testManager(t)
	defer m.Close(time.Second)
	job, err := m.StartWithOptions("python3", []string{"-I", "-S", "-c", "import os,tempfile; from pathlib import Path; p=Path(tempfile.gettempdir()); print(p,flush=True); f=tempfile.NamedTemporaryFile(); f.write(b'job-private'); f.flush(); print(oct(p.stat().st_mode & 0o777),flush=True)"}, work, StartOptions{PrivateTemp: true})
	if err != nil {
		t.Fatal(err)
	}
	if s := waitForTerminal(t, m, job.ID); s.State != Done {
		t.Fatalf("private temp job: %+v", s)
	}
	out, err := m.Read(job.ID, "stdout", 0, MaxReadLimit)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(m.StateDir(), job.ID, "tmp") + "\n0o700\n"
	if out.Text != want {
		t.Fatalf("temporary files escaped job directory: got %q want %q", out.Text, want)
	}
	if _, err := m.StartWithOptions("env", nil, work, StartOptions{PrivateTemp: true, Env: map[string]string{"TMPDIR": work}}); err == nil {
		t.Fatal("caller overrode managed temporary directory")
	}
}

func TestReapPreservesExitedJobWithLiveDescendant(t *testing.T) {
	m, work := testManager(t)
	defer m.Close(time.Second)
	job, err := m.StartWithOptions("python3", []string{"-I", "-S", "-c", "import subprocess; subprocess.Popen(['sleep','10'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)"}, work, StartOptions{PrivateTemp: true})
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-job.PID, syscall.SIGKILL)
	s := waitForTerminal(t, m, job.ID)
	if s.State != Done {
		t.Fatalf("parent did not exit: %+v", s)
	}
	if n, err := m.Reap(0, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("deleted live descendant evidence: removed=%d error=%v", n, err)
	}
	if _, err := m.Get(job.ID); err != nil {
		t.Fatal("owner receipt lost", err)
	}
	if _, err := os.Stat(filepath.Join(m.StateDir(), job.ID, "tmp")); err != nil {
		t.Fatal("live descendant temp removed", err)
	}
	_ = syscall.Kill(-job.PID, syscall.SIGKILL)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := m.Reap(0, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job not reaped after descendant exit")
}

func TestReaderFindEffectsRejectedBeforeLaunch(t *testing.T) {
	for _, action := range []string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls"} {
		t.Run(action, func(t *testing.T) {
			work := t.TempDir()
			target := filepath.Join(work, "target")
			if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			p := policy.Default()
			p.AllowedWorkdirs = []string{work}
			if err := p.SetDefaultProfile(policy.ProfileReader); err != nil {
				t.Fatal(err)
			}
			m, err := New(t.TempDir(), p)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{target, action}
			switch action {
			case "-exec", "-execdir", "-ok", "-okdir":
				args = append(args, "touch", filepath.Join(work, "marker"), ";")
			case "-fprint", "-fprint0", "-fls":
				args = append(args, filepath.Join(work, "marker"))
			case "-fprintf":
				args = append(args, filepath.Join(work, "marker"), "%p")
			}
			if job, err := m.Start("find", args, work); err == nil {
				waitForTerminal(t, m, job.ID)
				t.Fatal("reader started find with side effects")
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "preserve" {
				t.Fatalf("target changed: %q %v", data, err)
			}
			if _, err := os.Lstat(filepath.Join(work, "marker")); !os.IsNotExist(err) {
				t.Fatalf("unexpected effect: %v", err)
			}
		})
	}
}

func TestProjectActivityReadOnlyNeverReconciles(t *testing.T) {
	m, work := testManager(t)
	j := &Job{ID: "old", State: Unknown, Workdir: work, PID: 1, BootID: "previous-boot", StartTicks: 1}
	m.jobs[j.ID] = j
	got := m.ProjectActivityReadOnly(work, 16)
	if got.Count != 1 || got.UnknownCount != 1 || j.ResourcesReleased || !j.ReconciledAt.IsZero() {
		t.Fatalf("read reconciled a job: %+v %+v", got, j)
	}
}

func TestPolicyHintPointsProjectRunnersToProjectTest(t *testing.T) {
	base := errors.New(`command "node" class privileged is not allowed by policy`)
	if err := withPolicyHint(base, "/usr/bin/node"); !errors.Is(err, base) || !strings.Contains(err.Error(), "project_test") {
		t.Fatalf("%v", err)
	}
	if err := withPolicyHint(base, "rm"); err != base {
		t.Fatalf("hint on an unrelated command: %v", err)
	}
}
