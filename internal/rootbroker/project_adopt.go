package rootbroker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"mcpshka/internal/repoadopt"
)

// project-adopt SESSION OWNER preview|apply REPO SOURCE|- [PLAN_SHA256]
// creates /root/repos/<name>, moves /root/<name> there, or hands an existing
// root-owned project to the service user, under the owner's Face ID window.
// The broker cannot see /root (ProtectHome), so the installed binary does the
// walk as root in a bounded transient unit and prints the pinned plan.
func validateProjectAdopt(args []string) error {
	if len(args) < 6 || len(args) > 7 || args[0] != "project-adopt" || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" {
		return errors.New("project-adopt requires session, owner, action, repository and source")
	}
	if (args[3] != "preview" || len(args) != 6) && (args[3] != "apply" || len(args) != 7 || !validHex(args[6], 64)) {
		return errors.New("project-adopt is preview, or apply with the previewed plan_sha256")
	}
	if err := repoadopt.ValidateTarget(args[4]); err != nil {
		return err
	}
	if args[5] != "-" {
		return repoadopt.ValidateSource(args[5])
	}
	return nil
}

func projectAdoptServiceArgs(args []string) []string {
	digest := sha256.Sum256([]byte(strings.Join(args[3:], "\x00")))
	return append([]string{fmt.Sprintf("--unit=mcpshka-project-adopt-%x", digest[:10]), "--quiet", "--collect", "--wait", "--pipe",
		"--property=Type=exec", "--property=User=root", "--property=Group=root",
		"--property=RuntimeMaxSec=300s", "--property=MemoryMax=512M", "--property=TasksMax=16",
		"--property=Nice=10", "--property=IOSchedulingClass=idle", "--property=UMask=0077",
		"--setenv=HOME=/nonexistent", installedMCPshka, "project-adopt"}, args[3:]...)
}

func runProjectAdopt(ctx context.Context, args []string) ([]byte, error) {
	if err := validateProjectAdopt(args); err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable(installedMCPshka); err != nil {
		return nil, err
	}
	lock, _, err := adminLegacyLeaseSession(args, time.Now())
	if err != nil {
		return nil, err
	}
	// Held for the walk: one handover at a time, and a revoked window
	// cannot race an apply already admitted.
	defer lock.Close()
	child, cancel := context.WithTimeout(ctx, 320*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/bin/systemd-run", projectAdoptServiceArgs(args)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	out := &limitedBuffer{limit: 64 << 10}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil || out.truncated {
		if args[3] == "apply" {
			return nil, fmt.Errorf("project handover outcome UNKNOWN; preview again before any retry: %.4096s", stderr.Bytes())
		}
		return nil, fmt.Errorf("project preview failed: %.4096s", stderr.Bytes())
	}
	return out.Bytes(), nil
}
