package rootbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"mcpshka/internal/diagprovider"
	"mcpshka/internal/diagrelease"
)

// diag-release SESSION OWNER COMMIT VERSION installs a DIAG build under the
// owner's Face ID window (until now DIAG was copied to /usr/local/bin by
// hand). The root worker takes the commit only if DIAG's main contains it,
// checks its declared version and verified external CI artifacts, smokes
// the new binary as nobody, swaps diag and diagd keeping .previous, restarts
// diagd and rolls back if the installed pair does not answer. The result is
// public JSON in diagrelease.Results; diag_release_status reads it.

var diagVersion = diagrelease.Version

func validateDiagInstall(args []string) error {
	if len(args) != 5 || args[0] != "diag-release" || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" ||
		!validHex(args[3], 40) || !diagVersion.MatchString(args[4]) {
		return errors.New("diag-release requires session, owner, 40-hex commit and version")
	}
	return nil
}

func runDiagRelease(ctx context.Context, args []string) ([]byte, error) {
	if err := validateDiagInstall(args); err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-broker"); err != nil {
		return nil, err
	}
	lock, _, err := adminLegacyLeaseSession(args, time.Now())
	if err != nil {
		return nil, err
	}
	lock.Close()
	unit := diagprovider.ReleaseUnit(args[4])
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", diagprovider.ReleaseUnitArgs(unit, args[3], args[4])...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("diag release not started (unit %s): %.1024s", unit, bytes.TrimSpace(out))
	}
	return json.Marshal(DiagReleaseResult{Version: args[4], Commit: args[3], Unit: unit, State: "STARTED"})
}

// DiagReleaseResult is the worker's public report (internal/diagrelease).
type DiagReleaseResult = diagrelease.Result

// RunDiagReleaseWorker runs the root worker (internal/diagrelease).
func RunDiagReleaseWorker(args []string) ([]byte, error) { return diagrelease.RunWorker(args) }

func fileSHA(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hexSHA(raw)
}
