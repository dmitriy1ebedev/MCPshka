package executor

import (
	"context"
	"errors"
	"mcpshka/internal/integrationqueue"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func integrationApprovalFixture(t *testing.T) (*Controller, Manifest, integrationqueue.RuntimeItem) {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	launch := strings.Repeat("d", 32)
	identity := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + launch + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	ctx := context.Background()
	for _, err := range []error{c.Create(p), c.Reserve(ctx, p, launch), c.Register(ctx, p, launch, identity)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := c.recordResult(ctx, ExecutionResult{Pin: p, LaunchID: launch, Identity: identity, ExitCode: 0, ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}); err != nil {
		t.Fatal(err)
	}
	source := &SourceSnapshot{Repo: "/root/repos/test-source", Commit: strings.Repeat("1", 40), Package: "internal/integrationqueue", Digest: strings.Repeat("2", 64)}
	m := Manifest{Purpose: "project-test-v1", Pin: p, Source: source, Runs: map[string]RunSpec{launch: {Profile: "project-test-v1", Command: []string{"go", "test", "-p", "1", "./internal/integrationqueue"}, WorkspaceDevice: 1, WorkspaceInode: 2}}}
	item := integrationqueue.RuntimeItem{Request: integrationqueue.Request{Owner: "operator-a", Path: source.Repo, ID: strings.Repeat("3", 32), Branch: "candidate", Commit: source.Commit, TargetBranch: "main", TargetHead: strings.Repeat("4", 40), RoadmapSHA: strings.Repeat("5", 64), Scope: "P7"}, ExecutionID: strings.Repeat("6", 32), State: integrationqueue.Pending}
	return c, m, item
}

func TestIntegrationApprovalColdManifestMustMatch(t *testing.T) {
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	review := IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.recordIntegrationApproval(ctx, m, item, repo, review); err != nil {
		t.Fatal(err)
	}
	cold := New(c.db)
	if _, err := cold.inspectIntegrationApprovalManifest(ctx, m, item, repo); err != nil {
		t.Fatal(err)
	}
	cases := map[string]Manifest{"missing": {}, "canary": m, "source": m, "generation": m, "command": m}
	x := cases["canary"]
	x.Purpose = "canary-v1"
	cases["canary"] = x
	x = cases["source"]
	source := *x.Source
	source.Digest = strings.Repeat("9", 64)
	x.Source = &source
	cases["source"] = x
	x = cases["generation"]
	x.Pin.Generation++
	cases["generation"] = x
	x = cases["command"]
	x.Runs = map[string]RunSpec{strings.Repeat("d", 32): {Profile: "project-test-v1", Command: []string{"go", "test", "-p", "1", "./internal/other"}, WorkspaceDevice: 1, WorkspaceInode: 2}}
	cases["command"] = x
	for name, changed := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := cold.inspectIntegrationApprovalManifest(ctx, changed, item, repo); !errors.Is(err, ErrHold) {
				t.Fatalf("changed sealed manifest authorized effect: %v", err)
			}
		})
	}
}

func TestIntegrationApprovalRootBoundaryRejectsDaemon(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("daemon boundary requires the non-root production identity")
	}
	_, _, item := integrationApprovalFixture(t)
	if _, err := ApproveIntegration(context.Background(), strings.Repeat("a", 32), item, IntegrationReview{Reviewer: "caller", Digest: strings.Repeat("7", 64)}); !errors.Is(err, ErrHold) {
		t.Fatalf("daemon minted approval: %v", err)
	}
}

func TestIntegrationApprovalRequiresFencedSuccessAndExactCandidate(t *testing.T) {
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	review := IntegrationReview{Reviewer: "root-reviewed-code", Digest: strings.Repeat("7", 64)}
	if _, err := c.recordIntegrationApproval(ctx, m, item, repo, review); !errors.Is(err, ErrHold) {
		t.Fatalf("unfenced test authorized Git: %v", err)
	}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	changed := item
	changed.Request.Commit = strings.Repeat("8", 40)
	if _, err := c.recordIntegrationApproval(ctx, m, changed, repo, review); !errors.Is(err, ErrHold) {
		t.Fatalf("borrowed candidate test accepted: %v", err)
	}
	a, err := c.recordIntegrationApproval(ctx, m, item, repo, review)
	if err != nil {
		t.Fatal(err)
	}
	cold := New(c.db)
	got, err := cold.inspectIntegrationApproval(ctx, item, repo)
	if err != nil || got.TestPin != m.Pin || got.Review != review || got.ID != a.ID {
		t.Fatalf("lost cold approval: %+v %v", got, err)
	}
	changed = item
	changed.Request.Owner = "operator-b"
	if _, err := cold.inspectIntegrationApproval(ctx, changed, repo); !errors.Is(err, ErrHold) {
		t.Fatal("approval borrowed by another owner")
	}
	changed = item
	changed.Request.RoadmapSHA = strings.Repeat("9", 64)
	if _, err := cold.inspectIntegrationApproval(ctx, changed, repo); !errors.Is(err, ErrHold) {
		t.Fatal("roadmap approval reused")
	}
	changedRepo := repo
	changedRepo.Inode++
	if _, err := cold.inspectIntegrationApproval(ctx, item, changedRepo); !errors.Is(err, ErrHold) {
		t.Fatal("replacement repository authorized")
	}
	changedReview := review
	changedReview.Digest = strings.Repeat("8", 64)
	if _, err := c.recordIntegrationApproval(ctx, m, item, repo, changedReview); !errors.Is(err, ErrHold) {
		t.Fatal("immutable review overwritten")
	}
}

func TestIntegrationApprovalCannotUseCanaryOrFailedTest(t *testing.T) {
	c, m, item := integrationApprovalFixture(t)
	ctx := context.Background()
	repo := RepositoryIdentity{CommonDir: "/root/repos/test-source/.git", Device: 1, Inode: 2}
	review := IntegrationReview{Reviewer: "root-review", Digest: strings.Repeat("7", 64)}
	if err := c.Close(ctx, m.Pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Fence(ctx, m.Pin, &fenceStub{}); err != nil {
		t.Fatal(err)
	}
	m.Purpose = "canary-v1"
	if _, err := c.recordIntegrationApproval(ctx, m, item, repo, review); !errors.Is(err, ErrHold) {
		t.Fatal("canary counted as candidate tests")
	}
	m.Purpose = "project-test-v1"
	raw, err := c.db.GetContext(ctx, resultKey(m.Pin))
	if err != nil {
		t.Fatal(err)
	}
	// A terminal failed result must never authorize integration, even though its
	// process is fully fenced. Persisted result corruption also fails closed.
	bad := strings.Replace(string(raw), "\"exit_code\":0", "\"exit_code\":1", 1)
	if ok, err := c.db.Replace(resultKey(m.Pin), raw, []byte(bad)); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := c.recordIntegrationApproval(ctx, m, item, repo, review); !errors.Is(err, ErrHold) {
		t.Fatal("failed test authorized integration")
	}
}
