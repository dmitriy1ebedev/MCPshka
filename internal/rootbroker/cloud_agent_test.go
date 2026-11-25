package rootbroker

import (
	"context"
	"encoding/json"
	"mcpshka/internal/cloudtasks"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudAgentPythonContracts(t *testing.T) {
	path, err := filepath.Abs("../../packaging/cloud/test_dispatch.py")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-I", "-B", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("offline adapter contracts: %v\n%s", err, out)
	}
}

func TestCloudAgentAdminBoundary(t *testing.T) {
	p := cloudtasks.DispatchRequest{ID: "dispatch-one", Owner: "owner", Provider: "codex_cloud", Commit: strings.Repeat("a", 40), Prompt: "Fix", Acceptance: "PASS"}
	raw, _ := json.Marshal(p)
	good := []string{"cloud-agent-start", strings.Repeat("a", 32), "owner", string(raw)}
	if err := validateCloudAgent(good); err != nil {
		t.Fatal(err)
	}
	for i, value := range map[int]string{0: "agent-task", 1: "", 2: "another-owner", 3: string(raw) + "{}"} {
		bad := append([]string(nil), good...)
		bad[i] = value
		if validateCloudAgent(bad) == nil {
			t.Fatalf("unsafe cloud request %+v", bad)
		}
		if i == 1 {
			if _, err := runCloudAgent(context.Background(), bad); err == nil {
				t.Fatal("missing Face ID reached adapter")
			}
		}
	}
	bad := append([]string(nil), good...)
	bad[2] = "local-stdio"
	if validateCloudAgent(bad) == nil {
		t.Fatal("stdio inherited owner Face ID")
	}
}
