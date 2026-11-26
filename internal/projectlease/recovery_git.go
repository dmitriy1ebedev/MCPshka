package projectlease

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// ValidateGitStatusInputs rejects external filters and gitlinks before a trusted
// constructor uses status. Its own Git commands disable hooks and fsmonitor.
func ValidateGitStatusInputs(ctx context.Context, path string) error {
	return rejectRecoveryGitExecutables(ctx, path)
}

// Call status only after the sealed directories and process fence are verified.
func rejectRecoveryGitExecutables(ctx context.Context, path string) error {
	cmd := retirementGitCommand(ctx, path, "config", "--get-regexp", `^filter\..*\.(clean|process)$`)
	err := cmd.Run()
	if err == nil {
		return errors.New("executable Git filter configured: HOLD")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return errors.New("Git filter proof unavailable: HOLD")
	}
	for _, args := range [][]string{{"ls-files", "--format=%(objectmode)"}, {"ls-tree", "-r", "--format=%(objectmode)", "HEAD"}} {
		modes, err := retirementGit(ctx, path, args...)
		if err != nil {
			return err
		}
		for _, mode := range strings.Fields(modes) {
			if mode == "160000" {
				return errors.New("Git gitlink/submodule configuration is not accepted for root recovery: HOLD")
			}
		}
	}
	return nil
}
