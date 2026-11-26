package rootbroker

import (
	"mcpshka/internal/executor"
	"reflect"
	"strings"
	"testing"
)

func recipePrepareArgs() []string {
	return []string{"prepare-recipe", strings.Repeat("a", 32), "/root/repos/diag", strings.Repeat("b", 40), "300"}
}

func TestMCP232LegacyWorktreeMayReconcileButNeverPrepare(t *testing.T) {
	args := recipePrepareArgs()
	args[2] = "/root/repos/wt/lane-old-recipe"
	if err := validateExecutor(args); err == nil {
		t.Fatal("legacy path admitted to sealed preparation")
	}
	args[0] = "recipe-status"
	if err := validateExecutor(args); err == nil {
		t.Fatal("legacy path admitted to normal recipe status")
	}
	args[0] = "abandon-recipe"
	if err := validateExecutor(args); err != nil {
		t.Fatalf("legacy preparation cannot be reconciled: %v", err)
	}
	if args[2] != "/root/repos/wt/lane-old-recipe" {
		t.Fatal("validation lost the original path")
	}
	args[2] = "/root/repos/wt/../lane"
	if err := validateExecutor(args); err == nil {
		t.Fatal("noncanonical legacy path admitted")
	}
}

func TestRecipePrepareContractAndUnit(t *testing.T) {
	a := recipePrepareArgs()
	unit, args, err := recipePrepareServiceArgs(a)
	if err != nil {
		t.Fatal(err)
	}
	other, again, err := recipePrepareServiceArgs(a)
	if err != nil || other != unit || !reflect.DeepEqual(args, again) {
		t.Fatal("unstable preparation identity")
	}
	if validateExecutor(a) != nil {
		t.Fatal("prepare action not routed")
	}
	text := strings.Join(args, "\n")
	for _, wanted := range []string{"StandardOutput=null", "StandardError=journal", "--no-block", "RuntimeMaxSec=245s", "MemoryMax=256M", "TasksMax=16", "CPUQuota=100%", "KillMode=control-group", "TemporaryFileSystem=/root:mode=0711", "BindReadOnlyPaths=/root/repos -/root/.local/share/go/pkg/mod\n", "ReadWritePaths=/var/lib/mcpshka-executor", "PrivateNetwork=yes", "RestrictAddressFamilies=AF_UNIX", "RestrictNamespaces=yes", "AmbientCapabilities=CAP_SETUID CAP_SETGID", "CapabilityBoundingSet=CAP_DAC_OVERRIDE CAP_SETUID CAP_SETGID CAP_CHOWN CAP_FOWNER", "recipe-prepare-worker"} {
		if !strings.Contains(text, wanted) {
			t.Fatal("missing boundary", wanted)
		}
	}
	for _, forbidden := range []string{"--wait", "--pipe", "--collect", "CAP_SYS_ADMIN", "ProtectHome=no", "lease-recovery-worker"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("unexpected preparation authority", forbidden)
		}
	}
	want := append([]string{executorHelper, "recipe-prepare-worker"}, a...)
	if !reflect.DeepEqual(args[len(args)-len(want):], want) {
		t.Fatal("arbitrary worker entry")
	}
	p, launch, err := RecipePrepareIdentity(a[1])
	if err != nil || p.PermitID == launch || p.Generation != 1 {
		t.Fatal("invalid sealed identity")
	}
	a[3] = strings.Repeat("c", 40)
	changed, _, err := recipePrepareServiceArgs(a)
	if err != nil || changed == unit {
		t.Fatal("different source reused unit")
	}
}

func TestRecipePrepareRejectsCallerAuthority(t *testing.T) {
	for _, change := range []struct {
		i int
		v string
	}{{0, "prepare"}, {1, "../bootstrap"}, {1, strings.Repeat("A", 32)}, {2, "/root/repos"}, {2, "/root/repos/diag/"}, {2, "/root/repos/a/../diag"}, {2, "/root/repos/diag/child"}, {2, "/root/repos/diag:rw"}, {2, "/root/repos/diag%h"}, {2, "/etc"}, {3, "HEAD"}, {3, strings.Repeat("B", 40)}, {4, "0"}, {4, "-1"}, {4, "21601"}, {4, "0300"}, {4, "+300"}, {4, "300 "}} {
		a := recipePrepareArgs()
		a[change.i] = change.v
		if validateRecipePrepare(a) == nil || validateExecutor(a) == nil {
			t.Fatal("unsafe preparation accepted", a)
		}
	}
	tree := append(recipePrepareArgs(), "sh", "tests/run_safe.sh")
	if validateExecutor(tree) != nil {
		t.Fatal("registered tree argv not routed")
	}
	if pkg, argv := recipeSource(tree); pkg != executor.TreeTestPackage || !reflect.DeepEqual(argv, []string{"sh", "tests/run_safe.sh"}) {
		t.Fatal("tree source misread", pkg, argv)
	}
	if pkg, argv := recipeSource(recipePrepareArgs()); pkg != "make:test" || !reflect.DeepEqual(argv, []string{"make", "test"}) {
		t.Fatal("legacy source changed", pkg, argv)
	}
	for _, bad := range [][]string{{"/bin/sh"}, {"./../x"}, {"sh", "a\nb"}} {
		if validateExecutor(append(recipePrepareArgs(), bad...)) == nil {
			t.Fatal("unsafe tree argv accepted", bad)
		}
	}
	unitA, _, _ := recipePrepareServiceArgs(tree)
	unitB, _, _ := recipePrepareServiceArgs(recipePrepareArgs())
	if unitA == unitB {
		t.Fatal("argv not bound into constructor unit identity")
	}
	abandon := append([]string{"abandon-recipe"}, recipePrepareArgs()[1:]...)
	if validateExecutor(abandon) != nil {
		t.Fatal("abandon action not routed")
	}
	if validateExecutor(append(abandon, "/bin/sh")) == nil {
		t.Fatal("abandon accepted extra authority")
	}
	for _, a := range [][]string{nil, append(recipePrepareArgs(), "/usr/bin/make")} {
		if validateRecipePrepare(a) == nil {
			t.Fatal("arbitrary command accepted")
		}
	}
	if _, _, err := RecipePrepareIdentity("bad"); err == nil {
		t.Fatal("invalid bootstrap accepted")
	}
}

func TestRecipeSealedTimeoutCannotChangeOnReplay(t *testing.T) {
	a := recipePrepareArgs()
	p, launch, _ := RecipePrepareIdentity(a[1])
	spec := executor.RunSpec{Profile: "recipe-test-v1", Command: []string{"make", "test"}, TimeoutSeconds: 300, WorkspaceDevice: 1, WorkspaceInode: 1}
	m := executor.Manifest{Purpose: "project-recipe-test-v1", Pin: p, Source: &executor.SourceSnapshot{Repo: a[2], Commit: a[3], Package: "make:test"}, Runs: map[string]executor.RunSpec{launch: spec}}
	if !recipePreparedManifestMatches(m, a) {
		t.Fatal("matching sealed timeout rejected")
	}
	a[4] = "301"
	if recipePreparedManifestMatches(m, a) {
		t.Fatal("changed timeout reused same bootstrap")
	}
	args, err := recipeTestUnitArgs(p.PermitID, launch, "make:test", spec)
	if err != nil || !strings.Contains(strings.Join(args, " "), "RuntimeMaxSec=21900s") || strings.Contains(strings.Join(args, " "), "RuntimeMaxSec=1800s") {
		t.Fatal("unit ignored sealed TTL", err, args)
	}
	if !strings.Contains(strings.Join(args, " "), "PrivateNetwork=yes") {
		t.Fatal("suite without a dependency environment must stay off the network")
	}
	envSpec := spec
	envSpec.EnvBuild = &executor.EnvSpec{Kind: "python", Key: strings.Repeat("a", 32), Files: []string{"requirements.txt"}}
	envArgs, _ := recipeTestUnitArgs(p.PermitID, launch, "make:test", envSpec)
	if joined := strings.Join(envArgs, " "); !strings.Contains(joined, "PrivateNetwork=no") || !strings.Contains(joined, "CAP_CHOWN CAP_FOWNER") {
		t.Fatal("environment build unit lacks network or chown authority", joined)
	}
	joined := strings.Join(envArgs, " ")
	for _, want := range []string{"BindPaths=-" + executor.EnvRoot + "/" + strings.Repeat("a", 32) + ":" + executor.EnvReadyMountPath, "BindPaths=-" + executor.EnvRoot + "/" + strings.Repeat("a", 32) + ".partial:" + executor.EnvBuildMountPath} {
		if !strings.Contains(joined, want) {
			t.Fatal("environment bind missing", want)
		}
	}
	if !strings.HasSuffix(joined, "executor-worker "+p.PermitID+" "+launch) {
		t.Fatal("binds must precede the fixed helper arguments", joined)
	}
	// A private runtime directory must be available to the root helper despite
	// ProtectHome=yes. Never expose /root/repos or make the bind writable.
	private := "/root/repos/ug2/private/engine-test-0.1.a.23"
	runtimeSpec := spec
	runtimeSpec.Runtime = []executor.RuntimeBind{{Path: private, Target: private}}
	runtimeArgs, err := recipeTestUnitArgs(p.PermitID, launch, "make:test", runtimeSpec)
	if err != nil {
		t.Fatal(err)
	}
	runtimeJoined := strings.Join(runtimeArgs, "\n")
	if !strings.Contains(runtimeJoined, "--property=ProtectHome=tmpfs") ||
		strings.Contains(runtimeJoined, "--property=ProtectHome=yes") ||
		!strings.Contains(runtimeJoined, "--property=BindReadOnlyPaths="+private+":"+private) ||
		strings.Contains(runtimeJoined, "--property=BindReadOnlyPaths=/root/repos\n") ||
		strings.Contains(runtimeJoined, "--property=BindPaths="+private) {
		t.Fatal("runtime bind must be exact and read-only under ProtectHome", runtimeJoined)
	}
	if strings.Index(runtimeJoined, "--property=BindReadOnlyPaths="+private) >
		strings.Index(runtimeJoined, "executor-worker") {
		t.Fatal("runtime bind must precede fixed helper arguments")
	}
	if !strings.Contains(strings.Join(args, " "), "ProtectHome=yes") {
		t.Fatal("without a private runtime, the worker must keep ProtectHome=yes")
	}
	optRuntime := runtimeSpec
	optRuntime.Runtime = []executor.RuntimeBind{{Path: "/opt/lane/venv", Target: "/opt/lane/venv"}}
	optArgs, err := recipeTestUnitArgs(p.PermitID, launch, "make:test", optRuntime)
	if err != nil || !strings.Contains(strings.Join(optArgs, " "), "ProtectHome=yes") {
		t.Fatal("non-home runtime must retain ProtectHome=yes", err)
	}
	badRuntime := runtimeSpec
	badRuntime.Runtime = []executor.RuntimeBind{{Path: private, Target: "/root/repos/ug2/private/../other"}}
	if _, err := recipeTestUnitArgs(p.PermitID, launch, "make:test", badRuntime); err == nil {
		t.Fatal("non-canonical runtime target accepted")
	}
	if strings.Contains(strings.Join(args, " "), "CAP_CHOWN") {
		t.Fatal("suite without an environment must not gain chown authority")
	}
	for _, want := range []string{"CPUQuota=200%", "MemoryMax=1600M", "Nice=10", "IOSchedulingClass=idle", "BindPaths=" + executor.ModuleScratchRoot + "/" + p.PermitID} {
		if !strings.Contains(strings.Join(args, " "), want) {
			t.Fatal("recipe unit lacks build budget", want)
		}
	}
	for _, ttl := range []int64{0, -1, 21601} {
		spec.TimeoutSeconds = ttl
		if _, err := recipeTestUnitArgs(p.PermitID, launch, "make:test", spec); err == nil {
			t.Fatal("invalid unit TTL", ttl)
		}
	}
}

func TestRecipeExtrasRoundTripAndBoundary(t *testing.T) {
	extra := EncodeRecipeExtras([]string{"/opt/lane/venv-current"}, map[string]string{"LANE_TEST_PYTHON": "/opt/lane/venv-current/bin/python"})
	args := append(recipePrepareArgs(), extra, "sh", "tests/run_safe.sh")
	if validateExecutor(args) != nil {
		t.Fatal("extras not routed")
	}
	got, argv, err := recipeTail(args)
	if err != nil || got.Paths[0] != "/opt/lane/venv-current" || got.Env["LANE_TEST_PYTHON"] == "" || len(argv) != 2 || argv[0] != "sh" {
		t.Fatal("extras round trip", got, argv, err)
	}
	if EncodeRecipeExtras(nil, nil) != "" {
		t.Fatal("empty extras must add no argument")
	}
	for _, bad := range []string{"spec=!!", "spec=" + "e30"} {
		if validateExecutor(append(recipePrepareArgs(), bad, "sh")) == nil {
			t.Fatal("bad extras accepted", bad)
		}
	}
	unitA, _, _ := recipePrepareServiceArgs(args)
	unitB, _, _ := recipePrepareServiceArgs(append(recipePrepareArgs(), "sh", "tests/run_safe.sh"))
	if unitA == unitB {
		t.Fatal("extras not bound into constructor identity")
	}
}

func TestRecipeStatusUsesPreparationBoundary(t *testing.T) {
	args := append([]string{"recipe-status"}, recipePrepareArgs()[1:]...)
	if validateExecutor(args) != nil {
		t.Fatal("status action rejected")
	}
	for _, bad := range [][]string{args[:4], append(args, "/bin/sh")} {
		if validateExecutor(bad) == nil {
			t.Fatal("invalid status authority accepted")
		}
	}
}
