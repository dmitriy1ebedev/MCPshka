package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/distributedci"
	"mcpshka/internal/opencore"
)

func TestPublicShardRefusesSecretsBeforePush(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, remote := filepath.Join(dir, "source"), filepath.Join(dir, "public.git")
	git := func(path string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v", args[0], err)
		}
		return strings.TrimSpace(string(raw))
	}
	put := func(name, body string) {
		t.Helper()
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() string {
		git(repo, "add", ".")
		git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.org", "commit", "-qm", "fixture")
		return git(repo, "rev-parse", "HEAD")
	}
	git(dir, "init", "--bare", remote)
	git(dir, "init", repo)
	target := opencore.Target{Site: "github", URL: remote, Auth: "none"}
	cfg, _ := json.Marshal(opencore.Config{Targets: []opencore.Target{target}})
	put(opencore.ConfigPath, string(cfg))
	put("candidate.txt", "safe")
	base := commit()
	rules := []opencore.Rule{{From: "192.168.66.125", To: "192.168.77.10"}}
	s, err := opencore.StageCI(ctx, repo, base, rules)
	if err != nil {
		t.Fatal(err)
	}
	tree, digest := s.Tree, s.Manifest.Digest
	defer s.Close()
	for _, tc := range []struct{ name, content string }{
		{"token", "ghp_" + strings.Repeat("A", 40)},
		{"key", "-----BEGIN " + "PRIVATE KEY-----\n" + strings.Repeat("A", 64)},
		{"owner address", "192.168.66.125"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git(repo, "checkout", "--detach", base)
			put("candidate.txt", tc.content)
			rev := commit()
			if export, err := opencore.StageCI(ctx, repo, rev, rules); err == nil {
				export.Close()
				t.Fatal("injected input passed the export gate")
			}
			p := distributedci.Plan{Commit: rev, Tree: tree, Digest: digest, Snapshot: strings.Repeat("a", 40)}
			if err := publishPublicShard(ctx, repo, filepath.Join(dir, "journal"), p, 0, target, s); !errors.Is(err, distributedci.ErrEvidence) {
				t.Fatalf("expected HOLD, got %v", err)
			}
			if got := git(repo, "ls-remote", "--heads", remote, distributedci.ShardRef(p, 0)); got != "" {
				t.Fatal("blocked snapshot created a remote ref")
			}
			if _, err := os.Stat(filepath.Join(dir, "journal")); !os.IsNotExist(err) {
				t.Fatal("blocked snapshot reserved a publication")
			}
		})
	}
}
