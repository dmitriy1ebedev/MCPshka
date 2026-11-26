package approvedtask_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpshka/internal/approvedplan"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/executor"
	"mcpshka/internal/pipeline"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectrelease"
	"mcpshka/internal/tools/approvedtask"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func export(t *testing.T, base, repo, head string) (string, string) {
	t.Helper()
	sum := sha256.Sum256([]byte("package"))
	sha := hex.EncodeToString(sum[:])
	m := pipeline.PipelineManifest{Schema: "mcpshka.pipeline/v1", Project: repo, HEAD: head, Tree: "tree", ProfileDigest: "profile", Stage: "package", State: "PASS", FinishedAt: time.Now().UTC(), Artifacts: []executor.PipelineArtifact{{Path: "out/app.bin", SHA256: sha, Bytes: 7}}}
	identity, err := json.Marshal([]string{m.Project, m.HEAD, m.Tree, m.ProfileDigest, m.Stage})
	must(t, err)
	id := sha256.Sum256(identity)
	dir := filepath.Join(base, hex.EncodeToString(id[:]))
	m.ManifestPath = filepath.Join(dir, "manifest.json")
	path := filepath.Join(dir, "out/app.bin")
	must(t, os.MkdirAll(filepath.Dir(path), 0700))
	must(t, os.WriteFile(path, []byte("package"), 0600))
	raw, err := json.Marshal(m)
	must(t, err)
	must(t, os.WriteFile(m.ManifestPath, raw, 0600))
	return path, sha
}

// All three public consumers must share identity, digest and alias checks.
func TestArtifactConsumersShareVerifiedExports(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial", "--allow-empty"}} {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	raw, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	must(t, err)
	head := strings.TrimSpace(string(raw))
	previous := pipeline.Exports
	t.Cleanup(func() { pipeline.Exports = previous })
	for _, tc := range []string{"valid", "sha", "manifest-sha", "head", "project", "missing", "symlink", "parent-symlink", "manifest-symlink", "traversal"} {
		t.Run(tc, func(t *testing.T) {
			pipeline.Exports = t.TempDir()
			sourceRepo, sourceHead := repo, head
			if tc == "head" {
				sourceHead = strings.Repeat("0", 40)
			}
			if tc == "project" {
				sourceRepo = repo + "-other"
			}
			path, sha := export(t, pipeline.Exports, sourceRepo, sourceHead)
			rel := "out/app.bin"
			switch tc {
			case "sha":
				must(t, os.WriteFile(path, []byte("changed"), 0600))
			case "manifest-sha":
				manifest := filepath.Join(filepath.Dir(filepath.Dir(path)), "manifest.json")
				raw, err := os.ReadFile(manifest)
				must(t, err)
				must(t, os.WriteFile(manifest, []byte(strings.ReplaceAll(string(raw), sha, strings.Repeat("0", 64))), 0600))
			case "missing":
				must(t, os.Remove(path))
			case "symlink":
				other := filepath.Join(t.TempDir(), "same")
				must(t, os.WriteFile(other, []byte("package"), 0600))
				must(t, os.Remove(path))
				must(t, os.Symlink(other, path))
			case "parent-symlink":
				dir := filepath.Dir(path)
				moved := dir + "-real"
				must(t, os.Rename(dir, moved))
				must(t, os.Symlink(moved, dir))
			case "manifest-symlink":
				m := filepath.Join(filepath.Dir(filepath.Dir(path)), "manifest.json")
				must(t, os.Rename(m, m+".real"))
				must(t, os.Symlink(m+".real", m))
			case "traversal":
				rel = "../out/app.bin"
			}
			consumers := []struct {
				name    string
				resolve func() (string, error)
			}{
				{name: "approved_task_prepare", resolve: func() (string, error) {
					p, err := approvedtask.ResolveArtifacts(approvedplan.Plan{Workdir: repo, Args: []string{"pipeline:" + rel}, Inputs: []string{"pipeline:" + rel}})
					if err != nil {
						return "", err
					}
					if len(p.Inputs) != 1 || p.Inputs[0] != p.Args[0] || p.InputSHAs[p.Args[0]] != sha {
						t.Fatalf("unsealed plan: %+v", p)
					}
					argsOnly, err := approvedtask.ResolveArtifacts(approvedplan.Plan{Workdir: repo, Args: []string{"pipeline:" + rel}})
					if err != nil || len(argsOnly.Inputs) != 1 || argsOnly.InputSHAs[path] != sha {
						t.Fatalf("argument not pinned: %+v %v", argsOnly, err)
					}
					for _, key := range []string{"pipeline:" + rel, path} {
						if _, err := approvedtask.ResolveArtifacts(approvedplan.Plan{Workdir: repo, Inputs: []string{"pipeline:" + rel}, InputSHAs: map[string]string{key: "wrong"}}); err == nil {
							t.Fatal("caller SHA replaced")
						}
					}
					return p.Args[0], nil
				}},
				{name: "project_release", resolve: func() (string, error) { return projectrelease.ExpandArtifacts(repo, head, "{artifact:"+rel+"}") }},
				{name: "artifact_get", resolve: func() (string, error) {
					subdir := filepath.Join(repo, "allowed-subdir")
					must(t, os.MkdirAll(subdir, 0700))
					restricted := artifacts.New(&policy.Policy{AllowedWorkdirs: []string{subdir}}, time.Minute, 4)
					if _, err := restricted.CreatePipelineFor(subdir, rel, "owner"); err == nil {
						t.Fatal("allowed subdirectory exposed parent repository export")
					}
					m := artifacts.New(&policy.Policy{AllowedWorkdirs: []string{repo}}, time.Minute, 4)
					if _, err := m.CreateFor(path, "owner"); err == nil {
						t.Fatal("private pipeline store exposed as regular path")
					}
					ticket, err := m.CreatePipelineFor(repo, rel, "owner")
					if err != nil {
						return "", err
					}
					if ticket.SHA256 != sha {
						t.Fatal("ticket SHA not sealed")
					}
					if _, f, err := m.OpenFor(ticket.ID, "other"); err == nil {
						f.Close()
						t.Fatal("principal escaped")
					}
					_, f, err := m.OpenFor(ticket.ID, "owner")
					if err != nil {
						return "", err
					}
					body, err := io.ReadAll(f)
					f.Close()
					must(t, err)
					if string(body) != "package" {
						t.Fatal("wrong downloaded bytes")
					}
					must(t, os.WriteFile(path, []byte("changed"), 0600))
					if _, f, err := m.OpenFor(ticket.ID, "owner"); err == nil {
						f.Close()
						t.Fatal("changed export downloadable")
					}
					return ticket.SourcePath, nil
				}},
			}
			for _, consumer := range consumers {
				t.Run(consumer.name, func(t *testing.T) {
					got, err := consumer.resolve()
					if tc == "valid" {
						if err != nil || got != path {
							t.Fatalf("got %q: %v", got, err)
						}
						return
					}
					if err == nil {
						t.Fatal("unsafe export accepted")
					}
					if !strings.Contains(err.Error(), "next_safe_action = project_pipeline path="+repo+" stages=[package]") || !strings.Contains(err.Error(), "same path "+rel) {
						t.Fatalf("missing recovery: %v", err)
					}
				})
			}
		})
	}
}

func TestPipelineArgumentsBecomePinnedInputs(t *testing.T) {
	// Resolving a pipeline reference must not mutate the caller's plan on failure.
	p := approvedplan.Plan{Workdir: t.TempDir(), Args: []string{"pipeline:out/app.bin"}, InputSHAs: map[string]string{"old": "sha"}}
	_, _ = approvedtask.ResolveArtifacts(p)
	if p.Args[0] != "pipeline:out/app.bin" || p.InputSHAs["old"] != "sha" {
		t.Fatal("caller plan mutated")
	}
}
