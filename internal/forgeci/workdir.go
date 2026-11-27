package forgeci

import (
	"context"
	"errors"
	"mcpshka/internal/failure"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Workdir validates pinned sibling Git checkout for immutable CI reads.
func Workdir(workdir, root, mode, leaseToken, commit string) (string, error) {
	if workdir == "" || workdir == root {
		return root, nil
	}
	reject := func() (string, error) {
		return "", failure.Rejected("FORGE_CI_WORKTREE", "forge_ci",
			"invalid sibling CI scope: no job started; retain protected main writer and reconcile the holder",
			errors.New("requires an immediate non-symlink sibling of the protected Git root, matching Git store and exact HEAD, a holder token, and immutable plan/publish/status"))
	}
	path := filepath.Clean(workdir)
	if path != workdir || !filepath.IsAbs(path) || path == root ||
		filepath.Dir(path) != filepath.Dir(root) || leaseToken == "" ||
		(mode != "plan" && mode != "publish" && mode != "status") {
		return reject()
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return reject()
	}
	git := func(dir string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		argv := append([]string{"-C", dir}, args...)
		raw, err := exec.CommandContext(ctx, "/usr/bin/git", argv...).Output()
		return strings.TrimSpace(string(raw)), err
	}
	rootCommon, err := git(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return reject()
	}
	siblingCommon, err := git(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || siblingCommon != rootCommon {
		return reject()
	}
	head, err := git(path, "rev-parse", "HEAD")
	if err != nil || head != commit {
		return reject()
	}
	return path, nil
}
