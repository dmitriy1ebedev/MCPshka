package forgeci

import (
	"encoding/json"
	"fmt"
	"mcpshka/internal/distributedci"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDistributedWorkflowContracts(t *testing.T) {
	if _, e := os.Stat(filepath.Join("..", "..", ".github", "workflows", "distributed.yml")); os.IsNotExist(e) {
		t.Skip("workflow configs are outside the sealed module snapshot")
	}
	if raw, _ := os.ReadFile(filepath.Join("..", "..", ".sourcecraft", "ci.yaml")); strings.HasPrefix(string(raw), "# Open core CI") {
		t.Skip("public open-core export: the private distributed CI configs are replaced")
	}
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Fatal(e)
	}
	script := filepath.Join("..", "..", "packaging", "ci", "test_distributed.py")
	out, e := exec.Command(python, "-B", script).CombinedOutput()
	if e != nil {
		t.Fatalf("%v: %s", e, out)
	}
}
func TestDistributedArgvAndJobScope(t *testing.T) {
	for _, mode := range []string{"plan", "publish", "status"} {
		a, e := Argv("distributed", mode, strings.Repeat("a", 40))
		if e != nil || len(a) != 5 || a[0] != Command {
			t.Fatal(a, e)
		}
	}
	if _, e := Argv("distributed", "mirror", strings.Repeat("a", 40)); e == nil {
		t.Fatal("distributed main write allowed")
	}
}

func TestDistributedStatusStaysCompactAfterFailover(t *testing.T) {
	packages := []string{}
	for i := 0; i < 600; i++ {
		packages = append(packages, fmt.Sprintf("mcpshka/pkg%04d", i))
	}
	p, e := distributedci.NewPlan(strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), packages, nil, nil, 3)
	if e != nil {
		t.Fatal(e)
	}
	r := distributedci.Report{Schema: distributedci.Schema, Plan: p, State: "PASS", Trust: "UNTRUSTED"}
	for i := range p.Shards {
		r.Shards = append(r.Shards, distributedci.Observation{Provider: "github", Shard: i, State: "PASS", Packages: p.Shards[i]})
	}
	b, e := json.Marshal(distributedStatus(r))
	if e != nil || len(b) > 4096 {
		t.Fatal("status duplicated the package inventory", len(b), e)
	}
	var got map[string]any
	if json.Unmarshal(b, &got) != nil || got["state"] != "PASS" || got["plan_hash"] != p.Hash() {
		t.Fatal(string(b))
	}
}

func TestDistributedProjectJobInput(t *testing.T) {
	for _, root := range []string{"/root/repos/diag", "/root/repos/lane"} {
		for _, mode := range []string{"plan", "publish", "status", "wait"} {
			args, opts, e := JobInput("distributed", mode, strings.Repeat("a", 40), "key", "private-holder", root)
			if e != nil || len(args) != 5 || args[4] != root || opts.HolderToken != "private-holder" {
				t.Fatal(args, e)
			}
		}
	}
	if _, _, e := JobInput("distributed", "publish", strings.Repeat("a", 40), "key", "", "/root/repos/../lane"); e == nil {
		t.Fatal("unclean project path")
	}
}
