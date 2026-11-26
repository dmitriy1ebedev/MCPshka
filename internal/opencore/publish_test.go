package opencore

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishCommitsOnPublicMainWithoutForce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git")
	}
	d := t.TempDir()
	remote := filepath.Join(d, "public.git")
	if err := exec.Command("git", "init", "-q", "--bare", "-b", "main", remote).Run(); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(d, "tree")
	os.MkdirAll(tree, 0o755)
	os.WriteFile(filepath.Join(tree, "README.md"), []byte("open\n"), 0o644)
	p := Publish{Tree: tree, Commit: strings.Repeat("a", 40), Digest: strings.Repeat("b", 64), Version: "0.19.1", GitDir: filepath.Join(d, "work.git"), Target: Target{Site: "local", URL: remote, Auth: "none"}}
	state := func() string {
		raw, err := p.Run(context.Background())
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil || err != nil {
			t.Fatal(string(raw), err)
		}
		return v["state"].(string)
	}
	if s := state(); s != "PUBLISHED" {
		t.Fatal(s)
	}
	if s := state(); s != "UNCHANGED" {
		t.Fatal(s)
	}
	os.WriteFile(filepath.Join(tree, "LICENSE"), []byte("Apache-2.0\n"), 0o644)
	if s := state(); s != "PUBLISHED" {
		t.Fatal(s)
	}
	n, _ := exec.Command("git", "--git-dir", remote, "rev-list", "--count", "main").Output()
	if strings.TrimSpace(string(n)) != "2" {
		t.Fatal("history", string(n))
	}
	p.Target.Auth = "basic:x"
	p.Target.TokenFile = filepath.Join(d, "missing")
	if raw, err := p.Run(context.Background()); err == nil || !strings.Contains(string(raw), "token_missing") {
		t.Fatal(string(raw))
	}
	p.Commit = "bad"
	if raw, err := p.Run(context.Background()); err == nil || !strings.Contains(string(raw), "invalid_input") {
		t.Fatal(string(raw))
	}
}
