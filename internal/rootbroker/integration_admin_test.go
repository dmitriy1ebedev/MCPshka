package rootbroker

import (
	"encoding/json"
	"strings"
	"testing"

	"mcpshka/internal/cievidence"
	"mcpshka/internal/distributedgate"
	"mcpshka/internal/executor"
	"mcpshka/internal/integrationqueue"
)

func pendingItemJSON(t *testing.T) string {
	t.Helper()
	item := integrationqueue.RuntimeItem{
		Request: integrationqueue.Request{Owner: "owner:test", Path: "/root/repos/mcpshka", ID: "claude-0-1-153", Branch: "claude/0.1.153", Commit: strings.Repeat("a", 40),
			TargetBranch: "main", TargetHead: strings.Repeat("b", 40), RoadmapSHA: strings.Repeat("c", 64), Scope: "P7"},
		ExecutionID: strings.Repeat("d", 32), State: integrationqueue.Pending}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestIntegrationAdminRequiresSessionAndFixedIdentities(t *testing.T) {
	session, owner := strings.Repeat("e", 32), "owner:test"
	prepare := []string{"integration-test-prepare", session, owner, "/root/repos/mcpshka", strings.Repeat("a", 40)}
	if err := validateIntegrationAdmin(prepare); err != nil {
		t.Fatal(err)
	}
	approve := []string{"integration-approve", session, owner, strings.Repeat("1", 32), strings.Repeat("2", 32), pendingItemJSON(t)}
	if err := validateIntegrationAdmin(approve); err != nil {
		t.Fatal(err)
	}
	bad := [][]string{
		{"integration-test-prepare", session, "local-stdio", "/root/repos/mcpshka", strings.Repeat("a", 40)},
		{"integration-test-prepare", "short", owner, "/root/repos/mcpshka", strings.Repeat("a", 40)},
		{"integration-test-prepare", session, owner, "/root/repos/../etc", strings.Repeat("a", 40)},
		{"integration-test-prepare", session, owner, "/root/repos/mcpshka/sub", strings.Repeat("a", 40)},
		{"integration-test-prepare", session, owner, "/root/repos/mcpshka", "HEAD"},
		{"integration-test-prepare", session, owner, "/root/repos/mcpshka", strings.Repeat("a", 40), "10"},
		{"integration-test-prepare", session, owner, "/root/repos/mcpshka", strings.Repeat("a", 40), "0"},
		{"integration-approve", session, owner, "store", strings.Repeat("2", 32), pendingItemJSON(t)},
		{"integration-approve", session, owner, strings.Repeat("1", 32), strings.Repeat("2", 32), `{"request":{}}`},
		{"integration-approve", session, owner, strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Replace(pendingItemJSON(t), `"pending"`, `"integrated"`, 1)},
		{"integration-approve", session, owner, strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Replace(pendingItemJSON(t), `{"request"`, `{"manifest":{},"request"`, 1)},
		{"integration-merge", session, owner},
	}
	for _, args := range bad {
		if validateIntegrationAdmin(args) == nil {
			t.Fatal("accepted", args)
		}
	}
}

func TestCrossProjectProviderEvidenceFailsBeforeLaunchingRootWorker(t *testing.T) {
	session, owner := strings.Repeat("e", 32), "owner:test"
	for _, action := range []string{"integration-gitverse-prepare", "integration-distributed-prepare"} {
		args := []string{action, session, owner, cievidence.VentaRoot, strings.Repeat("a", 40)}
		if err := validateIntegrationAdmin(args); err == nil || !strings.Contains(err.Error(), "MCP-269") {
			t.Fatalf("%s must reject unsupported external CI, err=%v", action, err)
		}
		if _, _, err := integrationAdminServiceArgs(args); err == nil {
			t.Fatalf("%s must not create a root preparation unit for foreign CI", action)
		}
	}
	for _, path := range []string{"/root/repos/other-project", "/root/repos/mcpshka-evil"} {
		args := []string{"integration-ci-prepare", session, owner, path, strings.Repeat("a", 40)}
		if err := validateIntegrationAdmin(args); err == nil {
			t.Fatalf("arbitrary external project must be rejected: %s", path)
		}
	}
	trusted := []string{"integration-ci-prepare", session, owner, cievidence.VentaRoot, strings.Repeat("a", 40)}
	if err := validateIntegrationAdmin(trusted); err != nil {
		t.Fatalf("compiled-in Venta GitHub project must be allowed to reach root fetch: %v", err)
	}
	if _, _, err := integrationAdminServiceArgs(trusted); err != nil {
		t.Fatalf("trusted provider cannot construct a restricted root worker: %v", err)
	}
	module := []string{"integration-test-prepare", session, owner, "/root/repos/other-go-project", strings.Repeat("a", 40)}
	if err := validateIntegrationAdmin(module); err != nil {
		t.Fatalf("root-gated module evidence for other Go projects must stay available: %v", err)
	}
}

func TestIntegrationAdminUnitsKeepFixedBoundaries(t *testing.T) {
	session, owner := strings.Repeat("e", 32), "owner:test"
	unit, prepare, err := integrationAdminServiceArgs([]string{"integration-test-prepare", session, owner, "/root/repos/mcpshka", strings.Repeat("a", 40)})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(prepare, "\n")
	for _, want := range []string{"--no-block", "BindReadOnlyPaths=/root/repos " + executor.ModuleCacheRoot, "ReadWritePaths=" + executor.AuthorityRoot, "PrivateNetwork=yes", "TemporaryFileSystem=/root:mode=0711", "NoNewPrivileges=yes", "integration-admin-worker\nintegration-test-prepare\n/root/repos/mcpshka\n" + strings.Repeat("a", 40) + "\n1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prepare unit missing %q", want)
		}
	}
	if strings.Contains(text, "--wait") || strings.Contains(text, session) || strings.Contains(text, owner) {
		t.Fatal("prepare must not block the broker or pass grant identities to the worker")
	}
	again, _, _ := integrationAdminServiceArgs([]string{"integration-test-prepare", session, owner, "/root/repos/mcpshka", strings.Repeat("a", 40)})
	if again != unit {
		t.Fatal("unstable preparation unit name")
	}
	_, approve, err := integrationAdminServiceArgs([]string{"integration-approve", session, owner, strings.Repeat("1", 32), strings.Repeat("2", 32), pendingItemJSON(t)})
	if err != nil {
		t.Fatal(err)
	}
	text = strings.Join(approve, "\n")
	if !strings.Contains(text, "--wait") || strings.Contains(text, "--no-block") || strings.Contains(text, executor.ModuleCacheRoot) || !strings.Contains(text, "integration-approve\n"+owner+"\n") {
		t.Fatal("approval unit boundaries changed", text)
	}
}

func TestModuleTestBootstrapIsPerRepositoryAndCommit(t *testing.T) {
	a := distributedgate.ModuleTestBootstrap("/root/repos/mcpshka", strings.Repeat("a", 40), "1")
	if a != distributedgate.ModuleTestBootstrap("/root/repos/mcpshka", strings.Repeat("a", 40), "") || a == distributedgate.ModuleTestBootstrap("/root/repos/mcpshka", strings.Repeat("b", 40), "1") || a == distributedgate.ModuleTestBootstrap("/root/repos/other", strings.Repeat("a", 40), "1") || a == distributedgate.ModuleTestBootstrap("/root/repos/mcpshka", strings.Repeat("a", 40), "2") || len(a) != 32 {
		t.Fatal("bootstrap must be stable per repository and commit")
	}
	if _, _, err := executor.ModuleTestIdentity(a); err != nil {
		t.Fatal(err)
	}
}

func TestModuleTestUnitBindsScratchAndYieldsToTheHost(t *testing.T) {
	permit, launch := strings.Repeat("1", 32), strings.Repeat("2", 32)
	args := moduleTestUnitArgs(permit, launch)
	text := strings.Join(args, "\n")
	for _, want := range []string{"BindPaths=" + executor.ModuleScratchRoot + "/" + permit + ":" + executor.ScratchMountPath,
		"BindPaths=" + executor.AuthorityRoot + "/workspaces/" + permit + ":" + executor.WorkspaceMountPath,
		"Nice=10", "IOSchedulingClass=idle", "RuntimeMaxSec=25200s", "MemoryMax=1600M", "CPUQuota=200%", "PrivateNetwork=yes"} {
		if !strings.Contains(text, want) {
			t.Fatalf("module unit missing %q", want)
		}
	}
	tail := args[len(args)-4:]
	if tail[0] != executorHelper || tail[1] != "executor-worker" || tail[2] != permit || tail[3] != launch {
		t.Fatal("helper invocation must stay last and unchanged", tail)
	}
}

func TestIntegrationCIPrepareIsTheOnlyNetworkedUnit(t *testing.T) {
	session, owner, commit := strings.Repeat("e", 32), "owner:test", strings.Repeat("a", 40)
	ci := []string{"integration-ci-prepare", session, owner, "/root/repos/mcpshka", commit}
	if err := validateIntegrationAdmin(ci); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{
		{"integration-ci-prepare", session, "local-stdio", "/root/repos/mcpshka", commit},
		{"integration-ci-prepare", session, owner, "/root/repos/../etc", commit},
		{"integration-ci-prepare", session, owner, "/root/repos/mcpshka", "main"},
		{"integration-ci-prepare", session, owner, "/root/repos/mcpshka", commit, "0"},
	} {
		if validateIntegrationAdmin(bad) == nil {
			t.Fatal("accepted", bad)
		}
	}
	_, args, err := integrationAdminServiceArgs(ci)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(args, "\n")
	for _, want := range []string{"--no-block", "PrivateNetwork=no", "RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6", "BindReadOnlyPaths=/root/repos " + cievidence.TokenPath, "TemporaryFileSystem=/root:mode=0711", "NoNewPrivileges=yes", "integration-admin-worker\nintegration-ci-prepare\n/root/repos/mcpshka\n" + commit + "\n1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("CI prepare unit missing %q", want)
		}
	}
	if strings.Contains(text, executor.ModuleCacheRoot) || strings.Contains(text, session) {
		t.Fatal("CI prepare unit gained module cache or grant identity")
	}
	_, module, _ := integrationAdminServiceArgs([]string{"integration-test-prepare", session, owner, "/root/repos/mcpshka", commit})
	text = strings.Join(module, "\n")
	if strings.Contains(text, "AF_INET") || strings.Contains(text, "PrivateNetwork=no") || strings.Contains(text, cievidence.TokenPath) {
		t.Fatal("module preparation gained network or the GitHub token")
	}
	if distributedgate.CIEvidenceBootstrap("/root/repos/mcpshka", commit, "1") == distributedgate.ModuleTestBootstrap("/root/repos/mcpshka", commit, "1") {
		t.Fatal("CI and module evidence share a permit identity")
	}
}

func TestLocalBuildPreparationOffline(t *testing.T) {
	args := []string{"release-build-prepare", strings.Repeat("a", 32), "owner", "/root/repos/mcpshka", strings.Repeat("b", 40)}
	_, command, err := integrationAdminServiceArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Join(command, " ")
	for _, want := range []string{"PrivateNetwork=yes", executor.ModuleCacheRoot, "release-build-prepare"} {
		if !strings.Contains(raw, want) {
			t.Fatal(want)
		}
	}
}

func TestLocalBuildAndModuleTestNeverShareBootstrapClaim(t *testing.T) {
	repo, commit := "/root/repos/mcpshka", strings.Repeat("a", 40)
	for _, attempt := range []string{"1", "2"} {
		build := distributedgate.LocalReleaseBootstrap(repo, commit, attempt)
		if build == distributedgate.ModuleTestBootstrap(repo, commit, attempt) || build == distributedgate.CIEvidenceBootstrap(repo, commit, attempt) || build == distributedgate.GitVerseEvidenceBootstrap(repo, commit, attempt) {
			t.Fatal("build consumed test bootstrap claim")
		}
	}
}

func TestPrepareRetainsFailedUnit(t *testing.T) {
	_, args, err := integrationAdminServiceArgs([]string{"integration-test-prepare", strings.Repeat("a", 32), "owner", "/root/repos/mcpshka", strings.Repeat("b", 40)})
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range args {
		if arg == "--collect" {
			t.Fatal("failed preparation would be collected")
		}
	}
}

func TestEarlierAttemptUnitsMatchLaunchedUnits(t *testing.T) {
	base := []string{"integration-gitverse-prepare", strings.Repeat("a", 32), "owner", "/root/repos/mcpshka", strings.Repeat("b", 40)}
	first, _, err := integrationAdminServiceArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := integrationAdminServiceArgs(append(append([]string(nil), base...), "2"))
	got := earlierAttemptUnits(append(append([]string(nil), base...), "3"))
	if len(got) != 2 || got[0] != first+".service" || got[1] != second+".service" {
		t.Fatalf("%v vs %s %s", got, first, second)
	}
	if earlierAttemptUnits(base) != nil {
		t.Fatal("attempt 1 has no earlier units")
	}
}
