package jobs

import (
	"encoding/json"
	"errors"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeExternal struct {
	name                   string
	starts                 int
	result                 ExternalObservation
	startErr, errorObserve error
	planned                bool
}

func (f *fakeExternal) Name() string { return f.name }
func (f *fakeExternal) Match(c string, a []string, r string) bool {
	return c == "make" && len(a) == 1 && a[0] == "test"
}
func (f *fakeExternal) Plan(id, c string, a []string, r string, o StartOptions) (json.RawMessage, error) {
	f.planned = true
	return json.RawMessage(`{"permit":"fixed"}`), nil
}
func (f *fakeExternal) Start(b json.RawMessage) error { f.starts++; return f.startErr }
func (f *fakeExternal) Observe(b json.RawMessage, cancel bool) (ExternalObservation, error) {
	if cancel {
		f.result = ExternalObservation{State: Cancelled, ExitCode: externalCode(137), ResourcesReleased: true}
	}
	return f.result, f.errorObserve
}
func externalCode(n int) *int { return &n }
func externalManager(t *testing.T) (*Manager, string, *fakeExternal) {
	t.Helper()
	m, w := testManager(t)
	p := policy.Default()
	p.AllowedWorkdirs = []string{w}
	m.policy = p
	f := &fakeExternal{name: "fake-v1", result: ExternalObservation{State: Running}}
	m.SetExternalRuntime(f)
	return m, w, f
}
func TestExternalLostStartDurableRestartNoReplay(t *testing.T) {
	m, w, f := externalManager(t)
	f.startErr = errors.New("lost reply")
	s, err := m.Start("make", []string{"test"}, w)
	if err != nil || s.State != Unknown || f.starts != 1 {
		t.Fatalf("start: %+v %v", s, err)
	}
	raw, err := m.Get(s.ID)
	if err != nil || raw.State != Running {
		t.Fatalf("observe: %+v %v", raw, err)
	}
	if err = m.Close(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	restored, err := New(m.root, m.policy)
	if err != nil {
		t.Fatal(err)
	}
	restored.SetExternalRuntime(f)
	got, err := restored.Get(s.ID)
	if err != nil || got.External == nil || got.State != Running || f.starts != 1 {
		t.Fatalf("restart replay/lost binding: %+v %v starts %d", got, err, f.starts)
	}
	f.result = ExternalObservation{State: Done, ExitCode: externalCode(0), ResourcesReleased: true, Output: "real result"}
	got, err = restored.Get(s.ID)
	if err != nil || got.State != Done || !got.ResourcesReleased {
		t.Fatalf("verified terminal: %+v %v", got, err)
	}
	out, err := restored.Read(s.ID, "stdout", 0, 16)
	if err != nil || out.Text != "real result" {
		t.Fatalf("output: %+v %v", out, err)
	}
}
func TestExternalExitWithoutFenceBlocksLeaseAndReap(t *testing.T) {
	m, w, f := externalManager(t)
	s, err := m.Start("make", []string{"test"}, w)
	if err != nil {
		t.Fatal(err)
	}
	f.result = ExternalObservation{State: Done, ExitCode: externalCode(0)}
	got, err := m.Get(s.ID)
	if err != nil || got.State != Unknown || got.ResourcesReleased {
		t.Fatalf("unfenced done: %+v %v", got, err)
	}
	called := false
	if err = m.WithIdleProject(w, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("unfenced job admitted writer")
	}
	if err = m.WithGoneOwner(w, s.ID, func(Snapshot) error { called = true; return nil }); err == nil || called {
		t.Fatal("unfenced owner released lease")
	}
	if n, err := m.Reap(0, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("reap unknown: %d %v", n, err)
	}
	f.result.ResourcesReleased = true
	if err = m.WithGoneOwner(w, s.ID, func(Snapshot) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("fenced owner recovery: %v", err)
	}
}
func TestExternalCancelAndRuntimeMismatch(t *testing.T) {
	m, w, f := externalManager(t)
	s, err := m.Start("make", []string{"test"}, w)
	if err != nil {
		t.Fatal(err)
	}
	m.SetExternalRuntime(&fakeExternal{name: "other"})
	if m.ExternalRuntimeName() != "other" {
		t.Fatalf("runtime name = %q", m.ExternalRuntimeName())
	}
	if _, err = m.Cancel(s.ID); err == nil {
		t.Fatal("different runtime accepted binding")
	}
	m.SetExternalRuntime(f)
	got, err := m.Cancel(s.ID)
	if err != nil || got.State != Cancelled || !got.ResourcesReleased {
		t.Fatalf("cancel: %+v %v", got, err)
	}
}
func TestExternalAdmissionHonorsLeaseAndNoNativeMake(t *testing.T) {
	m, w, f := externalManager(t)
	m.SetLeaseGuard(func(string, string) error { return errors.New("held") })
	if _, err := m.Start("make", []string{"test"}, w); err == nil || f.planned || f.starts != 0 {
		t.Fatal("lease guard bypassed")
	}
	m.SetLeaseGuard(nil)
	m.policy.DeniedCommands["make"] = struct{}{}
	if _, err := m.Start("make", []string{"test"}, w); err == nil || f.starts != 0 {
		t.Fatal("denylist bypassed")
	}
}

func TestExternalTerminalRequiresExitProofAndCopiesBinding(t *testing.T) {
	m, w, f := externalManager(t)
	s, err := m.Start("make", []string{"test"}, w)
	if err != nil {
		t.Fatal(err)
	}
	s.External.Runtime = "tampered"
	s.External.Binding[0] = '!'
	for _, o := range []ExternalObservation{{State: Done, ResourcesReleased: true}, {State: Done, ResourcesReleased: true, ExitCode: externalCode(2)}, {State: Unknown, ResourcesReleased: true, ExitCode: externalCode(0)}} {
		f.result = o
		got, err := m.Get(s.ID)
		if err != nil || got.State != Unknown || got.ResourcesReleased || got.ExitCode != nil {
			t.Fatalf("false terminal: %+v %v", got, err)
		}
	}
	f.errorObserve = errors.New("lost observation")
	if _, err = m.Get(s.ID); err == nil {
		t.Fatal("lost response not reported")
	}
	if err = m.WithIdleProject(w, func() error { return nil }); err == nil {
		t.Fatal("unknown observation admitted writer")
	}
}

func TestExternalManagedLaunchProofRecoversOnlyFencedResult(t *testing.T) {
	m, w, f := externalManager(t)
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	m.database = db
	id := "workflow-0123456789abcdef0123456789abcdef"
	s, err := m.StartWithOptions("make", []string{"test"}, w, StartOptions{ReservedID: id})
	if err != nil || f.starts != 1 || s.PID != 0 {
		t.Fatalf("managed external launch: %+v %v starts=%d", s, err, f.starts)
	}
	proof, err := db.Get(launchKey(id))
	if err != nil {
		t.Fatal(err)
	}
	var pinned Snapshot
	if err = json.Unmarshal(proof, &pinned); err != nil || pinned.External == nil {
		t.Fatalf("immutable binding: %+v %v", pinned, err)
	}
	delete(m.jobs, id)
	key := "jobs/" + id + "/" + metadataName
	raw, err := db.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Delete(key, raw); err != nil {
		t.Fatal(err)
	}
	called := false
	f.result = ExternalObservation{State: Done, ExitCode: externalCode(0)}
	err = m.withMissingReceipt(w, id, func() (bool, error) { return true, nil }, false, func(*Snapshot) error { called = true; return nil })
	if err == nil || called || f.starts != 1 {
		t.Fatalf("unfenced missing receipt recovery: %v called=%v starts=%d", err, called, f.starts)
	}
	delete(m.jobs, id)
	f.result.ResourcesReleased = true
	err = m.withMissingReceipt(w, id, func() (bool, error) { return true, nil }, false, func(s *Snapshot) error {
		called = true
		if s == nil || s.External == nil || s.State != Done || !s.ResourcesReleased {
			t.Fatalf("invalid recovered outcome: %+v", s)
		}
		return nil
	})
	if err != nil || !called || f.starts != 1 {
		t.Fatalf("fenced missing receipt recovery: %v called=%v starts=%d", err, called, f.starts)
	}
	if _, err = m.Get(id); err != nil {
		t.Fatalf("restored public receipt: %v", err)
	}
}

type delayedExternal struct {
	*fakeExternal
	entered      chan struct{}
	release      chan struct{}
	startsAtomic atomic.Int32
	observations atomic.Int32
}

func (f *delayedExternal) Start(json.RawMessage) error {
	if f.startsAtomic.Add(1) == 1 {
		close(f.entered)
		<-f.release
	}
	return nil
}
func (f *delayedExternal) Observe(json.RawMessage, bool) (ExternalObservation, error) {
	f.observations.Add(1)
	return ExternalObservation{State: Running}, nil
}
func TestExternalStartingDoesNotLockControlPlane(t *testing.T) {
	m, w, base := externalManager(t)
	f := &delayedExternal{fakeExternal: base, entered: make(chan struct{}), release: make(chan struct{})}
	m.SetExternalRuntime(f)
	firstResult := make(chan error, 1)
	go func() { _, err := m.Start("make", []string{"test"}, w); firstResult <- err }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("start not entered")
	}
	// Always release the fake backend, including on a failed assertion.
	defer func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	}()
	checked := make(chan error, 1)
	go func() {
		activity := m.ProjectActivity(w, 16)
		if activity.Count != 1 || len(activity.Jobs) != 1 {
			checked <- errors.New("dispatch not registered as pending")
			return
		}
		s, err := m.Get(activity.Jobs[0].ID)
		if err != nil || s.State != Unknown {
			checked <- errors.New("in-flight status unavailable")
			return
		}
		if f.observations.Load() != 0 {
			checked <- errors.New("in-flight start observed concurrently")
			return
		}
		other := t.TempDir()
		m.policy.AllowedWorkdirs = append(m.policy.AllowedWorkdirs, other)
		if _, err = m.Start("make", []string{"test"}, other); err != nil {
			checked <- err
			return
		}
		if err = m.Close(time.Millisecond); err != nil {
			checked <- err
			return
		}
		checked <- nil
	}()
	select {
	case err := <-checked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight external Start locked the control plane")
	}
	if f.startsAtomic.Load() != 2 {
		t.Fatalf("unexpected dispatch count %d", f.startsAtomic.Load())
	}
	// The deferred release unblocks the start after Close; verify the callback
	// cannot revive it to RUNNING in a separate assertion after explicit release.
	close(f.release)
	select {
	case err := <-firstResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("start did not return")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.starting != 0 {
		t.Fatalf("admission reservation leaked: %d", m.starting)
	}
	for _, j := range m.jobs {
		if j.State != Unknown || j.externalStarting {
			t.Fatalf("shutdown job revived: %+v", snapshot(j))
		}
	}
}

func TestHolderTokenUsesHolderGuardOnly(t *testing.T) {
	m, w, f := externalManager(t)
	m.SetLeaseGuard(func(string, string) error { return errors.New("held") })
	if _, err := m.StartWithOptions("make", []string{"test"}, w, StartOptions{HolderToken: "token"}); err == nil || f.starts != 0 {
		t.Fatal("holder token admitted without a holder guard")
	}
	var seen string
	m.SetHolderGuard(func(_, token string) error { seen = token; return errors.New("foreign") })
	if _, err := m.StartWithOptions("make", []string{"test"}, w, StartOptions{HolderToken: "token"}); err == nil || seen != "token" || f.starts != 0 {
		t.Fatal("foreign holder admitted or token not checked")
	}
	if _, err := m.StartWithOptions("make", []string{"test"}, w, StartOptions{HolderToken: "token", ReservedID: "workflow-" + strings.Repeat("a", 32)}); err == nil {
		t.Fatal("holder token combined with a managed identity")
	}
	m.SetHolderGuard(func(string, string) error { return nil })
	if _, err := m.StartWithOptions("make", []string{"test"}, w, StartOptions{HolderToken: "token"}); err != nil {
		t.Fatal("current holder blocked by its own lease", err)
	}
	if _, err := m.Start("make", []string{"test"}, w); err == nil {
		t.Fatal("tokenless job admitted under held lease")
	}
}

type preparingExternal struct {
	fakeExternal
	cancels []bool
}

// While the sandbox is still being prepared the runtime cannot fence yet and
// answers Running even to a cancel.
func (f *preparingExternal) Observe(b json.RawMessage, cancel bool) (ExternalObservation, error) {
	f.cancels = append(f.cancels, cancel)
	if cancel && len(f.cancels) > 1 {
		return ExternalObservation{State: Cancelled, ExitCode: externalCode(130), ResourcesReleased: true}, nil
	}
	return ExternalObservation{State: Running}, nil
}

// 08.11: a full test cancelled during preparation launched anyway, because the
// next status poll observed it without the cancel. The cancel must stick.
func TestExternalCancelDuringPrepareSticks(t *testing.T) {
	m, w, _ := externalManager(t)
	f := &preparingExternal{fakeExternal: fakeExternal{name: "fake-v1"}}
	m.SetExternalRuntime(f)
	s, err := m.Start("make", []string{"test"}, w)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Cancel(s.ID); got.State != Running {
		t.Fatalf("first cancel: %+v", got)
	}
	got, err := m.Get(s.ID)
	if err != nil || got.State != Cancelled || len(f.cancels) < 2 || !f.cancels[len(f.cancels)-1] {
		t.Fatalf("status poll dropped the cancel: %+v %v %v", got, err, f.cancels)
	}
}
