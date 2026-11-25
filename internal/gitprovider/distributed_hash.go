package gitprovider

import (
	"context"
	"os/exec"
	"strings"
)

func execCIHash(ctx context.Context, repo, body string) string {
	c := exec.CommandContext(ctx, "/usr/bin/git", "--no-replace-objects", "-c", "safe.directory="+repo, "-c", "core.hooksPath=/dev/null", "hash-object", "-t", "commit", "-w", "--stdin")
	c.Dir = repo
	c.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	c.Stdin = strings.NewReader(body)
	raw, e := c.Output()
	if e != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
