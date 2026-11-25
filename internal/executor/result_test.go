package executor

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/statestore"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResultSurvivesRestartAndNeedsFence(t *testing.T) {
	ctx := context.Background()
	db, e := statestore.Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	c := New(db)
	p := Pin{PermitID: strings.Repeat("a", 32), LeaseID: strings.Repeat("b", 32), ExecutionID: strings.Repeat("c", 32), Generation: 1}
	id := strings.Repeat("d", 32)
	identity := Identity{BootID: "433f15c2-05e7-40dd-855e-d82b4c8d7e7f", InvocationID: strings.Repeat("f", 32), Cgroup: "/system.slice/mcpshka-executor-" + id + ".service", CgroupDevice: 1, CgroupInode: 2, PID: 3, StartTicks: 4}
	for _, e := range []error{c.Create(p), c.Reserve(ctx, p, id), c.Register(ctx, p, id, identity)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	r := ExecutionResult{Pin: p, LaunchID: id, Identity: identity, ExitCode: 0, Output: "ok", ObservedAt: time.Now().UTC(), State: "EXITED_UNFENCED", Trust: "UNTRUSTED"}
	binary := r
	binary.Output = string([]byte{255})
	if !errors.Is(c.recordResult(ctx, binary), ErrHold) {
		t.Fatal("invalid UTF-8 poisons durable evidence")
	}
	bad := r
	bad.Identity.StartTicks++
	if !errors.Is(c.recordResult(ctx, bad), ErrHold) {
		t.Fatal("borrowed process proof accepted")
	}
	if e = c.recordResult(ctx, r); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(c.recordResult(ctx, r), ErrHold) {
		t.Fatal("immutable result overwritten")
	}
	c = New(db)
	s, e := c.Inspect(ctx, p)
	if e != nil || s.Result == nil || s.Result.State != "EXITED_UNFENCED" {
		t.Fatalf("premature completion or lost result %+v %v", s, e)
	}
	raw, _ := json.Marshal(s)
	var status map[string]any
	_ = json.Unmarshal(raw, &status)
	if status["next_action"] != "canary-fence "+p.PermitID+" "+id {
		t.Fatalf("missing fence action: %s", raw)
	}
	if e = c.Close(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e = c.CompleteFence(ctx, p); !errors.Is(e, ErrHold) {
		t.Fatal("result substituted for fence")
	}
	if e = c.Fence(ctx, p, &fenceStub{}); e != nil {
		t.Fatal(e)
	}
	s, e = c.Inspect(ctx, p)
	if e != nil || s.Result.State != "SUCCEEDED" {
		t.Fatalf("fenced outcome %+v %v", s, e)
	}
}
func TestReadonlyProjectProfileDiskBoundAndPythonDenied(t *testing.T) {
	s := RunSpec{Profile: "project-test-v1", WorkspaceDevice: 1, WorkspaceInode: 2, Command: []string{"go", "test", "-p", "1", "./internal/roadmapscan"}}
	if e := s.validate(); e != nil {
		t.Fatal(e)
	}
	raw := strings.Join(workerArgs(strings.Repeat("a", 32), s), " ")
	for _, want := range []string{"--ro-bind-fd 3 /workspace", "--size 268435456 --tmpfs /tmp", "--size 16777216 --tmpfs /run", "GOPROXY off", "GOMEMLIMIT 96MiB"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(raw, "--bind-fd 3") {
		t.Fatal("writable source")
	}
	s.Command = []string{"python3", "-I", "-c", "print(1)"}
	if s.validate() == nil {
		t.Fatal("project caller supplied program")
	}
}
func TestSnapshotDigestRejectsLinksAndDetectsChanges(t *testing.T) {
	p := t.TempDir()
	f := filepath.Join(p, "source.go")
	if e := os.WriteFile(f, []byte("package example"), 0600); e != nil {
		t.Fatal(e)
	}
	before, e := snapshotDigest(p)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(f, []byte("package changed"), 0600)
	after, e := snapshotDigest(p)
	if e != nil || before == after {
		t.Fatal("changed source undetected")
	}
	link := filepath.Join(p, "copy.go")
	if e = os.Link(f, link); e != nil {
		t.Fatal(e)
	}
	if _, e = snapshotDigest(p); !errors.Is(e, ErrHold) {
		t.Fatal("shared inode accepted")
	}
	os.Remove(link)
	os.Symlink(f, link)
	if _, e = snapshotDigest(p); !errors.Is(e, ErrHold) {
		t.Fatal("symlink accepted")
	}
}
