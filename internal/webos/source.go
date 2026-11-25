package webos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mcpshka/internal/pipeline"
)

// Package prefers the sealed pipeline export of the repo's HEAD over a loose
// repo file: a build left in the store must install without a manual copy
// (14.11: G5 IPK built, webos_deploy saw only the repo). It returns the file
// and a human description of where it came from.
func Package(repo, ipk string) (string, string, error) {
	head, gerr := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	h := strings.TrimSpace(string(head))
	var perr error = gerr
	if gerr == nil {
		file, m, err := pipeline.PackagedArtifact(pipeline.Exports, repo, h, filepath.Clean(ipk))
		if err == nil {
			return file, "project_pipeline package of " + m.HEAD[:12] + " (job " + m.JobID + ")", nil
		}
		perr = err
	}
	pkg := filepath.Join(repo, ipk)
	if _, err := os.Stat(pkg); err == nil {
		return pkg, "repo file", nil
	}
	return "", "", fmt.Errorf("%s: not in the repo and no verified package export of HEAD %s (%v); next_safe_action = project_pipeline path=%s stages=[package], then retry with the same path", ipk, h, perr, repo)
}
