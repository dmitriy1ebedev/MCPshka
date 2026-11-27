package mcpserver

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/authn"
)

func TestProjectContextProviderLanesSeparateAuthAndSnapshotIdentity(t *testing.T) {
	withIssueStore(t)
	m, _, in := yieldFixture(t)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = in.Path
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("config", "remote.origin.url", "https://fake-private-credential@github.com/team/canonical.git")
	run("config", "remote.snapshot.url", "git@gitverse.ru:team/snapshot.git")
	r := &toolRegistry{server: mcp.NewServer(&mcp.Implementation{Name: "lane-test", Version: "1"}, nil), tools: map[string]bool{}, readers: map[string]probeHandler{}}
	registerProjectContext(r, m)
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: "reader", Role: "reader", Scopes: []string{"project_context"}})
	read := func() ProjectContext {
		value, err := r.readers["project_context"](ctx, map[string]any{"path": in.Path})
		if err != nil {
			t.Fatal(err)
		}
		return value.(ProjectContext)
	}
	out := read()
	if len(out.ProviderLanes.Lanes) != 3 || out.ProviderLanes.Lanes[0].Repository != "team/canonical" || out.ProviderLanes.Lanes[1].Repository != "team/snapshot" || out.ProviderLanes.Lanes[2].Revision != out.Revision {
		t.Fatal(out.ProviderLanes)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "fake-private-credential") {
		t.Fatal("remote credential leaked")
	}
	for _, lane := range out.ProviderLanes.Lanes {
		if lane.Auth.API != "UNKNOWN" || lane.Auth.Git != "UNKNOWN" || lane.Auth.Plugin != "UNKNOWN" || lane.CIState != "UNKNOWN" {
			t.Fatal("configuration promoted to auth/CI evidence", lane)
		}
	}
	tree := out.ProviderLanes.SourceTree
	if tree == "" {
		t.Fatal("missing source tree")
	}
	sha := run("-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "commit-tree", tree, "-m", "parentless snapshot")
	run("update-ref", "HEAD", sha)
	again := read()
	if again.Revision == out.Revision || again.ProviderLanes.SourceTree != tree {
		t.Fatal("parentless source identity lost", again.ProviderLanes)
	}
}
