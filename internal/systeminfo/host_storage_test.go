package systeminfo

import (
	"context"
	"fmt"
	"mcpshka/internal/policy"
	"os"
	"path/filepath"
	"testing"
)

func TestHostInventoryRetainsUnknownAndProtectedRoles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "candidate"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}}
	inventory, err := InventoryHostStorage(context.Background(), p, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Paths) != 8 {
		t.Fatal("missing inventory categories")
	}
	if inventory.Paths[1].SizeBytes != 9 || !inventory.Paths[1].Complete {
		t.Fatal("wrong exact candidate evidence")
	}
	for _, x := range inventory.Paths[2:] {
		if x.State == "known" {
			t.Fatalf("privileged path marked known: %+v", x)
		}
	}
	plan := PlanHostHousekeeping(inventory)
	if plan.State != "HOLD" || plan.VerifiedReclaimBytes != 0 || len(plan.Blockers) == 0 {
		t.Fatalf("unsafe plan %+v", plan)
	}
	for _, x := range plan.Items {
		if x.Action == "delete" {
			t.Fatalf("unsafe cleanup candidate %+v", x)
		}
	}
}
func TestHostInventoryStopsAtBudget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cache")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < HostScanMaxEntries+1; i++ {
		if err := os.WriteFile(filepath.Join(path, fmt.Sprintf("%04d", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := &policy.Policy{AllowedWorkdirs: []string{root}}
	out := scanHostPath(context.Background(), p, path, "build_cache")
	if out.State != "partial" || out.Complete {
		t.Fatalf("truncated scan marked safe: %+v", out)
	}
}
func TestDockerEnvironmentIsKeyValueBased(t *testing.T) {
	a := []string{"A=1", "B=x=y", "EMPTY="}
	b := []string{"EMPTY=", "B=x=y", "A=1"}
	if !DockerEnvironmentEqual(a, b) {
		t.Fatal("permutation caused false difference")
	}
	if DockerEnvironmentEqual(a, []string{"A=1", "B=x=y", "EMPTY=changed"}) {
		t.Fatal("missed changed value")
	}
	if DockerEnvironmentEqual(a, []string{"A=1", "A=2", "B=x=y"}) {
		t.Fatal("ambiguous duplicate accepted")
	}
}

func TestHostMountEvidenceFindsSameDeviceBindAndEscapedPaths(t *testing.T) {
	evidence := []byte("11 1 0:7 / / rw - tmpfs tmpfs rw\n" +
		"12 11 0:7 / /srv/work\\040space/cache rw - tmpfs tmpfs rw\n")
	nested, err := hasNestedHostMount("/srv/work space", evidence)
	if err != nil || !nested {
		t.Fatalf("same-device bind must block inventory: nested=%v err=%v", nested, err)
	}
	nested, err = hasNestedHostMount("/srv/work space/cache", evidence)
	if err != nil || nested {
		t.Fatalf("mount exactly at inventory root is not nested: nested=%v err=%v", nested, err)
	}
	nested, err = hasNestedHostMount("/srv/other", evidence)
	if err != nil || nested {
		t.Fatalf("sibling mount incorrectly matched: nested=%v err=%v", nested, err)
	}
}

func TestHostMountEvidenceFailsClosedOnMalformedAndOversize(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("incomplete\n"),
		[]byte("11 1 0:7 / /srv/work\\09x rw - tmpfs tmpfs rw\n"),
		[]byte("11 1 0:7 / relative rw - tmpfs tmpfs rw\n"),
		make([]byte, hostMountInfoMaxBytes+1),
	}
	for _, evidence := range cases {
		if _, err := hasNestedHostMount("/srv/work", evidence); err == nil {
			t.Fatalf("invalid mount evidence accepted (bytes=%d)", len(evidence))
		}
	}
}
