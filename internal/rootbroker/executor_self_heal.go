package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"mcpshka/internal/executor"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/transientheal"
)

// Executor permits whose owner is provably gone (older than an hour, no live
// unit, no process carrying the permit, record pinned by SHA-256) blocked
// project_test/project_run until the owner ran the Face ID admin reconcile
// (ChatGPT audit 03.11.2026). The controller now asks the broker to fence
// exactly those, on its own: the same preview, the same proof rechecked
// under every admission lock at its CAS, the same audit receipt, with the
// authorization naming self-heal. Nothing is killed or stopped and an
// unprovable record stays for the owner.

const (
	selfHealStalePermits      = "self-heal-stale-permits"
	selfHealAuthorization     = "self-heal:proven-abandoned-permits:v1"
	selfHealStalePermitsEvery = 10 * time.Minute
)

// SelfHealStalePermitsResult is what one automatic pass did.
type SelfHealStalePermitsResult = selfheal.Result

var selfHealGate struct {
	sync.Mutex
	last time.Time
}

// selfHealAdmit spaces automatic passes; the owner's admin reconcile is not
// limited by this.
func selfHealAdmit(now time.Time) error {
	selfHealGate.Lock()
	defer selfHealGate.Unlock()
	if !selfHealGate.last.IsZero() && now.Sub(selfHealGate.last) < selfHealStalePermitsEvery {
		return fmt.Errorf("executor self-heal ran %s ago; nothing changed", now.Sub(selfHealGate.last).Round(time.Second))
	}
	selfHealGate.last = now
	return nil
}

func runExecutorSelfHeal(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 1 || args[0] != selfHealStalePermits {
		return nil, errors.New("executor-self-heal supports " + selfHealStalePermits + " only")
	}
	if err := selfHealAdmit(time.Now()); err != nil {
		return nil, err
	}
	unit, command, err := legacyLeaseServiceArgs(args)
	if err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-broker"); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", command...)
	out := &limitedBuffer{limit: 65536}
	cmd.Stdout = out
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("executor self-heal worker outcome UNKNOWN; the next pass rechecks every proof (unit %s): %s", unit, stderr.Bytes())
	}
	if out.truncated || !json.Valid(out.Bytes()) {
		return nil, errors.New("invalid executor self-heal worker result: UNKNOWN")
	}
	return out.Bytes(), nil
}

// selfHealStalePermitsWorker runs inside the fixed root worker unit.
func selfHealStalePermitsWorker() (SelfHealStalePermitsResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cfg, _ := LoadConfig("")
	reset := transientheal.ResetFailed(ctx, cfg.DockerRestart)
	preview, err := executor.PreviewStalePermitsAuthority(ctx)
	if err != nil {
		return SelfHealStalePermitsResult{}, err
	}
	out := SelfHealStalePermitsResult{Skipped: preview.Skipped, ResetFailed: reset, Leases: projectlease.AutoReleaseDefault(time.Now())}
	if len(preview.Plan.Permits) == 0 {
		return out, nil
	}
	res, err := executor.ReconcileStalePermitsAuthority(ctx, preview.Plan, selfHealAuthorization)
	if err != nil {
		return out, err
	}
	out.Reconciled = res.Reconciled
	return out, nil
}
