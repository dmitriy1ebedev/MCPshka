// Package ventagit reads the Venta candidate's local Git identity for root CI provenance.
package ventagit

import (
	"context"
	"errors"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/cievidence"
)

// LocalIdentity derives the candidate's sole local branch and source
// tree using a read-only, credential-free Git process. A caller cannot supply
// its own remote repository, checked job list, branch, or expected tree.
func LocalIdentity(parent context.Context, repo, commit string) (string, string, error) {
	if repo != cievidence.VentaRoot || len(commit) != 40 {
		return "", "", errors.New("external CI project root or commit invalid: HOLD")
	}
	for _, ch := range commit {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return "", "", errors.New("external CI commit not a SHA: HOLD")
		}
	}
	account, err := user.Lookup("mcpshka")
	if err != nil || account.Uid != "994" {
		return "", "", errors.New("external CI Git reader identity unavailable: HOLD")
	}
	group, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil || group == 0 {
		return "", "", errors.New("external CI Git reader group unavailable: HOLD")
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		fixed := []string{
			"--no-replace-objects",
			"-c", "safe.directory=" + repo,
			"-c", "protocol.allow=never",
			"-c", "core.fsmonitor=false",
			"-c", "core.hooksPath=/dev/null",
			"-C", repo,
		}
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append(fixed, args...)...)
		cmd.Env = []string{
			"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0",
			"GIT_PROTOCOL_FROM_USER=0",
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: 994, Gid: uint32(group),
		}}
		out, err := cmd.Output()
		if err != nil || len(out) > 4096 {
			return "", errors.New("external CI source Git read failed: HOLD")
		}
		return strings.TrimSpace(string(out)), nil
	}
	tree, err := run("rev-parse", "--verify", commit+"^{tree}")
	if err != nil || len(tree) != 40 {
		return "", "", errors.New("external CI source tree unavailable: HOLD")
	}
	branches, err := run("for-each-ref", "--format=%(refname:short)", "--points-at", commit, "refs/heads")
	if err != nil || branches == "" || strings.Contains(branches, "\n") || branches == "main" {
		return "", "", errors.New("external CI candidate branch ambiguous: HOLD")
	}
	// FetchVenta additionally validates the branch and compares it with
	// the authenticated PR head, so forged/moved branch names fail closed.
	return tree, branches, nil
}
