package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedTestSnapshotIncludesInternalDependencies(t *testing.T) {
	repo, snapshot := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Snapshot Test")
	git("config", "user.email", "snapshot@example.invalid")
	files := map[string]string{
		"go.mod":                             "module snapshot.test\n\ngo 1.26\n",
		"internal/shared/shared.go":          "package shared\nfunc Value() int { return 42 }\n",
		"internal/consumer/consumer_test.go": "package consumer\nimport (\"testing\"; \"snapshot.test/internal/shared\")\nfunc TestValue(t *testing.T) { if shared.Value() != 42 { t.Fatal(\"wrong committed dependency\") } }\n",
		"private-note.txt":                   "must not enter source snapshot\n",
	}
	for name, body := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-m", "source pin")
	commit := git("rev-parse", "HEAD")
	// The snapshot must resolve imports from the pinned commit, not dirty HEAD.
	if err := os.WriteFile(filepath.Join(repo, "internal/shared/shared.go"), []byte("broken dirty work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	list, err := projectTestTree(ctx, repo, commit, "internal/consumer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := materializeProjectTestSource(ctx, repo, "internal/consumer", snapshot, list); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "private-note.txt")); !os.IsNotExist(err) {
		t.Fatal("unrelated root file copied")
	}
	cmd := exec.Command("go", "test", "-p", "1", "./internal/consumer")
	cmd.Dir = snapshot
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("committed snapshot cannot resolve local imports: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "internal/shared/shared.go")); err != nil || string(b) != "broken dirty work\n" {
		t.Fatal("source worktree changed")
	}
}

func TestCommittedTestSnapshotRejectsDependencySymlinks(t *testing.T) {
	repo, snapshot := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Snapshot Test")
	git("config", "user.email", "snapshot@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module snapshot.test\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "internal/consumer"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal/consumer/consumer.go"), []byte("package consumer\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "internal/shared"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(repo, "internal/shared/leak")); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "symlink must be rejected")
	list, err := projectTestTree(context.Background(), repo, git("rev-parse", "HEAD"), "internal/consumer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := materializeProjectTestSource(context.Background(), repo, "internal/consumer", snapshot, list); err == nil {
		t.Fatal("dependency symlink accepted")
	}
	if _, err := os.Lstat(filepath.Join(snapshot, "internal/shared/leak")); !os.IsNotExist(err) {
		t.Fatal("dependency symlink materialized")
	}
}

func TestCommittedSnapshotCannotWriteThroughDestinationSymlink(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Snapshot Test")
	git("config", "user.email", "snapshot@example.invalid")
	for name, body := range map[string]string{
		"go.mod":                      "module snapshot.test\n\ngo 1.26\n",
		"internal/consumer/source.go": "package consumer\n",
	} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-m", "destination containment")
	list, err := projectTestTree(context.Background(), repo, git("rev-parse", "HEAD"), "internal/consumer")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		component string
		relative  bool
	}{
		{"internal", false}, {"internal/consumer", false},
		{"internal", true}, {"internal/consumer", true},
	} {
		t.Run(fmt.Sprintf("%s/relative=%t", tc.component, tc.relative), func(t *testing.T) {
			base, outside := t.TempDir(), t.TempDir()
			if err := os.Chmod(outside, 0700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(base, tc.component)
			if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
				t.Fatal(err)
			}
			target := outside
			if tc.relative {
				target, err = filepath.Rel(filepath.Dir(link), outside)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			_, materializeErr := materializeProjectTestSource(context.Background(), repo, "internal/consumer", base, list)
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("snapshot wrote outside its workspace before rejection")
			}
			info, err := os.Stat(outside)
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("outside permissions changed: %v %v", info, err)
			}
			if materializeErr == nil {
				t.Fatal("destination symlink accepted")
			}
		})
	}
}
