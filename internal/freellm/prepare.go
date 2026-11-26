package freellm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/llmgateway"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mcpshka/internal/issues"
)

func (c *Conveyor) prepare(ctx context.Context, f Fix, card issues.Issue, paths []string) (Fix, error) {
	if f.Root == "" {
		f.Project, f.Root = projectRoot(card.Project)
	}
	steps, err := c.plan(ctx, f, card, paths)
	if err != nil {
		return f, err
	}
	f.Steps = steps
	var wt struct{ Worktree struct{ Path, Head string } }
	key := "freellm:" + f.Card + ":" + f.At.Format("20060102")
	if err := c.call(ctx, "project_worktree", map[string]any{"op": "create", "path": f.Root, "branch": f.Branch, "base": "main", "idempotency_key": key + ":tree"}, &wt); err != nil {
		return f, err
	}
	f.Path, f.Base = wt.Worktree.Path, wt.Worktree.Head
	if err := c.save(f); err != nil {
		return f, err
	}
	var lease struct{ Token string }
	if err := c.call(ctx, "project_lease_acquire", map[string]any{"path": f.Path, "owner": "freellm:" + f.Card, "idempotency_key": key + ":lease"}, &lease); err != nil {
		return f, err
	}
	token := lease.Token
	if token == "" {
		return f, errors.New("lease receipt missing")
	}
	tokenPath := filepath.Join(c.Dir, f.Card+".lease-token")
	// Release only after a known terminal check. Timeout retains its resume token.
	release := true
	defer func() {
		if release {
			var ignored any
			if c.call(context.WithoutCancel(ctx), "project_lease_release", map[string]any{"path": f.Path, "token": token, "idempotency_key": key + ":release"}, &ignored) == nil {
				_ = os.Remove(tokenPath)
			}
		}
	}()
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		return f, err
	}
	packages, changed := []string{}, []string{}
	f.Quorum = true
	for step, paths := range steps {
		files, hashes := map[string]string{}, map[string]string{}
		for _, p := range paths {
			var file struct {
				Text, SHA256 string
				Truncated    bool
			}
			if err := c.call(ctx, "fs_read", map[string]any{"path": filepath.Join(f.Path, p), "limit": 65536}, &file); err != nil {
				// Create-only fs_write still refuses an existing unreadable file.
				files[p], hashes[p] = "", ""
				continue
			}
			if file.Truncated || file.SHA256 == "" {
				return f, errors.New("context exceeds bounded file budget")
			}
			files[p], hashes[p] = file.Text, file.SHA256
		}
		raw, _ := json.Marshal(map[string]any{"card": card.Title + "\n" + card.Body, "plan": steps, "step": step, "files": files})
		request := llmgateway.Redact(string(raw)).Text
		var proposal Proposal
		var quorum bool
		for attempt := 0; attempt < 2; attempt++ {
			comparison, compareErr := c.Compare(ctx, request)
			if compareErr != nil {
				return f, compareErr
			}
			proposal, quorum, err = Choose(comparison, files)
			if err == nil {
				break
			}
			if attempt == 1 {
				return f, err
			}
			request = llmgateway.Redact(string(raw) + "\nVALIDATION_ERROR: " + err.Error() +
				"\nReturn a complete corrected JSON edit proposal. Use only keys from files and exact old text occurring once.").Text
		}
		f.Quorum = f.Quorum && quorum
		// A blank existing file is still an existing file: create-only must
		// not overwrite it after a model supplies old="".
		for _, e := range proposal.Edits {
			if e.Old == "" && hashes[e.Path] != "" {
				return f, fmt.Errorf("create-only proposal targets existing file %q", e.Path)
			}
		}
		for i, e := range proposal.Edits {
			var patched any
			patchKey := fmt.Sprintf("%s:step:%d:patch:%d", key, step, i)
			var err error
			if e.Old == "" {
				if err = c.call(ctx, "fs_mkdir", map[string]any{"path": filepath.Dir(filepath.Join(f.Path, e.Path)), "lease_token": token, "idempotency_key": patchKey + ":dir"}, &patched); err != nil {
					return f, err
				}
				err = c.call(ctx, "fs_write", map[string]any{"path": filepath.Join(f.Path, e.Path), "expected_sha256": hashes[e.Path], "content_base64": base64.StdEncoding.EncodeToString([]byte(e.New)), "lease_token": token, "idempotency_key": patchKey}, &patched)
			} else {
				err = c.call(ctx, "fs_patch", map[string]any{"path": filepath.Join(f.Path, e.Path), "expected_sha256": hashes[e.Path], "old_text": e.Old, "new_text": e.New, "expected_replacements": 1, "lease_token": token, "idempotency_key": patchKey}, &patched)
			}
			if err != nil {
				return f, err
			}
			if strings.HasSuffix(e.Path, ".go") {
				pkg := "./" + filepath.ToSlash(filepath.Dir(e.Path))
				if !slices.Contains(packages, pkg) {
					packages = append(packages, pkg)
				}
			}
			if !slices.Contains(changed, e.Path) {
				changed = append(changed, e.Path)
			}
		}
	}
	var diff struct{ Text string }
	if err := c.call(ctx, "git_diff", map[string]any{"path": f.Path, "limit": 65536}, &diff); err != nil {
		return f, err
	}
	f.Diff = llmgateway.Redact(diff.Text).Text
	var commit struct{ Commit string }
	if err := c.call(ctx, "project_commit", map[string]any{"path": f.Path, "lease_token": token, "files": changed, "message": "FreeLLM: " + card.Title + "\n\nRefs " + f.Card, "idempotency_key": key + ":commit"}, &commit); err != nil {
		return f, err
	}
	f.Commit = commit.Commit
	// Once dispatch is attempted, an ambiguous reply keeps the lease and checkpoint.
	release = false
	f.State, f.Packages = "dispatching_test", packages
	if err := c.save(f); err != nil {
		return f, err
	}
	var test struct{ Job struct{ ID string } }
	if err := c.call(ctx, "project_test", map[string]any{"path": f.Path, "lease_token": token, "packages": packages, "idempotency_key": key + ":test", "timeout_seconds": 1800}, &test); err != nil {
		return f, err
	}
	f.Test, f.State = test.Job.ID, "testing"
	if err := c.save(f); err != nil {
		return f, err
	}
	return c.finish(ctx, f, token)
}
func (c *Conveyor) finish(ctx context.Context, f Fix, token string) (Fix, error) {
	for {
		var wait struct {
			Status struct {
				State    string
				ExitCode *int `json:"exit_code"`
			}
		}
		if err := c.call(ctx, "job_wait", map[string]any{"job_id": f.Test, "timeout_seconds": 40}, &wait); err != nil {
			return f, err
		}
		state := strings.ToLower(wait.Status.State)
		if state == "running" || state == "queued" {
			if ctx.Err() != nil {
				return f, ctx.Err()
			}
			continue
		}
		if state == "done" && wait.Status.ExitCode != nil && *wait.Status.ExitCode == 0 {
			f.State = "green"
		} else {
			f.State, f.Reason = "failed", "project_test did not pass"
		}
		if err := c.save(f); err != nil {
			return f, err
		}
		var released any
		if err := c.call(ctx, "project_lease_release", map[string]any{"path": f.Path, "token": token, "idempotency_key": "freellm:" + f.Card + ":" + f.Commit + ":finished"}, &released); err != nil {
			return f, err
		}
		_ = os.Remove(filepath.Join(c.Dir, f.Card+".lease-token"))
		return f, nil
	}
}
