package mcpserver

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
	"mcpshka/internal/deploy"
	"mcpshka/internal/failure"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/workflow"
)

func TestAtomicLeaseHandlerReplaysOnlyOriginalPrincipalAndActiveHolder(t *testing.T) {
	m, leases, existing := yieldFixture(t)
	t.Cleanup(func() { m.Close(time.Second) })
	if _, err := leases.Release(existing.Path, existing.Token); err != nil {
		t.Fatal(err)
	}
	store := idempotency.NewWithDatabase(filepath.Join(t.TempDir(), "idempotency"), m.Database())
	deps := &serverDeps{manager: m, leases: leases, pipelines: workflow.New(m), idempotencyStore: store}
	a := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner-a", Role: "operator", Scopes: []string{"*"}})
	b := authn.WithIdentity(context.Background(), authn.Identity{ID: "owner-b", Role: "operator", Scopes: []string{"*"}})
	in := ProjectLeaseAcquireInput{Path: existing.Path, Owner: "same-display-owner", IdempotencyKey: "same-known-key"}
	first, err := acquireProjectLease(a, deps, in)
	if err != nil || first.Token == "" {
		t.Fatal("first acquire", err)
	}
	other, err := acquireProjectLease(b, deps, in)
	if !failure.IsNoEffect(err) || other.Token != "" {
		t.Fatal("foreign principal obtained original credential or false UNKNOWN", err)
	}
	again, err := acquireProjectLease(a, deps, in)
	if err != nil || again.Token != first.Token || again.Status.ExecutionID != first.Status.ExecutionID || again.Status.Generation != first.Status.Generation {
		t.Fatal("lost reply replay changed original authority", err)
	}
	if !again.Status.AcquiredAt.Equal(first.Status.AcquiredAt) {
		t.Fatal("replay renewed lease")
	}
	if _, err := leases.Release(in.Path, first.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := idempotency.Prune(m.Database(), time.Now().Add(8*24*time.Hour), 500); err != nil {
		t.Fatal(err)
	}
	if out, err := acquireProjectLease(a, deps, in); err == nil || out.Token != "" {
		t.Fatal("released or pruned receipt recreated lease", err)
	}
	status, err := leases.Status(in.Path)
	if err != nil || status.Held {
		t.Fatal("replay stranded lease", status, err)
	}
}

func TestAtomicLeaseHandlerRejectsOtherOwnerAndThenReusesNoEffectKey(t *testing.T) {
	m, leases, held := yieldFixture(t)
	t.Cleanup(func() { m.Close(time.Second) })
	store := idempotency.NewWithDatabase(filepath.Join(t.TempDir(), "idempotency"), m.Database())
	d := &serverDeps{manager: m, leases: leases, pipelines: workflow.New(m), idempotencyStore: store}
	in := ProjectLeaseAcquireInput{Path: held.Path, Owner: "next", IdempotencyKey: "pending-only"}
	if _, err := acquireProjectLease(context.Background(), d, in); !failure.IsNoEffect(err) {
		t.Fatal("busy owner produced false UNKNOWN", err)
	}
	if _, err := leases.ValidateHolder(held.Path, held.Token); err != nil {
		t.Fatal("touched previous owner", err)
	}
	if _, err := leases.Release(held.Path, held.Token); err != nil {
		t.Fatal(err)
	}
	out, err := acquireProjectLease(context.Background(), d, in)
	if err != nil {
		t.Fatal("no-effect key could not continue after release", err)
	}
	if _, err := leases.Release(in.Path, out.Token); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseHandoffJournalCannotReturnSuccessorToPreviousPrincipal(t *testing.T) {
	m, leases, held := yieldFixture(t)
	t.Cleanup(func() { m.Close(time.Second) })
	store := idempotency.NewWithDatabase(filepath.Join(t.TempDir(), "idempotency"), m.Database())
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "handoff-principals", Version: "1"}, nil), store: store, leases: leases, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	d := &serverDeps{manager: m, leases: leases, pipelines: workflow.New(m), deploys: deploy.New(m.Policy(), filepath.Join(filepath.Dir(m.StateDir()), "deploys")), idempotencyStore: store}
	registerLeaseTools(r, d)
	a := authn.WithIdentity(context.Background(), authn.Identity{ID: "new-holder", Role: "operator", Scopes: []string{"*"}})
	b := authn.WithIdentity(context.Background(), authn.Identity{ID: "former-holder", Role: "operator", Scopes: []string{"*"}})
	args, err := object(projectlease.ResumeInput{Path: held.Path, Token: held.Token, Owner: "same-display-name", IdempotencyKey: "known-handoff-key"})
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := r.invokers["project_lease_resume"](a, args)
	if err != nil {
		t.Fatal("authorized handoff", err)
	}
	first := raw.(projectlease.AcquireResult)
	if first.Token == "" || first.Token == held.Token {
		t.Fatal("handoff did not rotate credential")
	}
	_, raw, err = r.invokers["project_lease_resume"](b, args)
	if err == nil {
		t.Fatal("previous principal recovered successor credential")
	}
	if out, ok := raw.(projectlease.AcquireResult); ok && out.Token != "" {
		t.Fatal("rejected handoff leaked successor")
	}
	_, raw, err = r.invokers["project_lease_resume"](a, args)
	if err != nil || raw.(projectlease.AcquireResult).Token != first.Token {
		t.Fatal("original holder could not reconcile its handoff", err)
	}
	if _, err := leases.ValidateHolder(held.Path, held.Token); err == nil {
		t.Fatal("old token still authorized writes")
	}
}

func TestLeaseReleaseWrappedCallWaitsForOriginalNativeJob(t *testing.T) {
	m, leases, held := yieldFixture(t)
	t.Cleanup(func() { m.Close(time.Second) })
	m.SetHolderGuard(leases.GuardHolder)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	job, err := m.StartWithOptions(sleep, []string{"2"}, held.Path, jobs.StartOptions{HolderToken: held.Token, Timeout: 8 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := idempotency.NewWithDatabase(filepath.Join(t.TempDir(), "idempotency"), m.Database())
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "release-idle", Version: "1"}, nil), store: store, leases: leases, readers: map[string]probeHandler{}, tools: map[string]bool{}}
	d := &serverDeps{manager: m, leases: leases, pipelines: workflow.New(m), deploys: deploy.New(m.Policy(), filepath.Join(filepath.Dir(m.StateDir()), "deploys")), idempotencyStore: store}
	registerLeaseTools(r, d)
	args, err := object(ProjectLeaseReleaseInput{Path: held.Path, Token: held.Token, IdempotencyKey: "same-release-after-idle"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.invokers["project_lease_release"](context.Background(), args); !failure.IsNoEffect(err) {
		t.Fatal("public release lost live job ownership or fabricated UNKNOWN", err)
	}
	if _, err := leases.ValidateHolder(held.Path, held.Token); err != nil {
		t.Fatal("live holder lost", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		j, err := m.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.State == jobs.Done || j.State == jobs.Failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err := r.invokers["project_lease_release"](context.Background(), args); err != nil {
		t.Fatal("same release key could not clean owned idle lease", err)
	}
}
