package rootbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/deploy"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"mcpshka/internal/projectprofile"
	"mcpshka/internal/statestore"
)

const legacyLeaseRepository = "/root/repos/mcpshka"

func validateLegacyLeaseRequest(args []string) error {
	if len(args) == 1 && args[0] == selfHealStalePermits {
		return nil
	}
	if len(args) != 4 || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" {
		return errors.New("invalid fixed lease recovery identity")
	}
	switch args[0] {
	case "preview-legacy-lease", "status-legacy-lease":
		if !staleLeaseRepository(args[3]) {
			return errors.New("bounded canonical registered repository required")
		}
	case "preview-interactive-fence":
		if !fenceRepairRepository(args[3]) {
			return errors.New("fixed fence repository required")
		}
	case "repair-interactive-fence":
		if _, err := decodeFenceRepairPlan(args[3]); err != nil {
			return err
		}
	case "preview-stale-permits":
		if args[3] != "all" {
			return errors.New("fixed executor authority scope required")
		}
	case "reconcile-stale-permits":
		if _, err := decodeStalePermitPlan(args[3]); err != nil {
			return err
		}
	case "preview-stale-lease":
		if !staleLeaseRepository(args[3]) {
			return errors.New("bounded canonical repository lease path required")
		}
	case "release-stale-lease":
		if _, err := decodeStaleReleasePlan(args[3]); err != nil {
			return err
		}
	case "recover-legacy-lease":
		if len(args[3]) > 8192 {
			return errors.New("lease recovery plan exceeds budget")
		}
		decoder := json.NewDecoder(bytes.NewReader([]byte(args[3])))
		decoder.DisallowUnknownFields()
		var p projectlease.RootSealedRecovery
		if decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF || !staleLeaseRepository(p.Path) || p.FenceRoot != p.Path || !recoveryCommonDir(p.Path, p.GitCommonDir) || p.Authorization != "" || p.LeaseID == "" || p.Owner == "" || (p.ExecutionID != "" && !validHex(p.ExecutionID, 32)) || !validHash(p.RecordSHA256) || (p.WorktreeSHA256 != "" && !validHash(p.WorktreeSHA256)) || !validHex(p.SnapshotTree, 40) || !validHash(p.JournalID) || p.NewOwner != "operator-root:mcpshka-one-tree-"+p.JournalID[:16] {
			return errors.New("invalid pinned project lease recovery plan")
		}
	default:
		return errors.New("unsupported lease recovery action")
	}
	return nil
}

func legacyLeaseServiceArgs(args []string) (string, []string, error) {
	if err := validateLegacyLeaseRequest(args); err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", nil, err
	}
	unit := "mcpshka-lease-recovery-" + shaHex(raw)[:32]
	repair := args[0] == "preview-interactive-fence" || args[0] == "repair-interactive-fence" || args[0] == "preview-stale-lease" || args[0] == "release-stale-lease"
	bind := "/root/repos"
	capabilities := "CAP_DAC_OVERRIDE CAP_SETUID CAP_SETGID CAP_SYS_PTRACE"
	if args[0] == "recover-legacy-lease" {
		capabilities += " CAP_CHOWN CAP_FOWNER"
	}
	if repair {
		// The fixed parent bind preserves evidence of child aliases. No project code runs here.
		bind = "/root/repos"
		capabilities += " CAP_CHOWN"
	}
	command := []string{"--unit=" + unit, "--collect", "--wait", "--pipe", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077",
		"--property=RuntimeMaxSec=45s", "--property=MemoryMax=128M", "--property=CPUQuota=50%", "--property=TasksMax=16", "--property=KillMode=control-group",
		"--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes", "--property=PrivateDevices=yes", "--property=PrivateNetwork=yes",
		"--property=ProtectSystem=strict", "--property=ProtectHome=tmpfs",
		"--property=BindReadOnlyPaths=" + bind,
		"--property=ReadWritePaths=/var/lib/mcpshka /var/lib/mcpshka-executor",
		"--property=CapabilityBoundingSet=" + capabilities,
		// statestore drops sqlite3 to the database owner. With NoNewPrivileges
		// and seccomp sandboxing PID1 otherwise denies that setresuid (EPERM),
		// leaving the worker without SQLite lease authority.
		"--property=AmbientCapabilities=CAP_SETUID CAP_SETGID",
		"--property=RestrictAddressFamilies=AF_UNIX", "--property=RestrictNamespaces=yes",
		"--property=ProtectKernelTunables=yes", "--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes",
		"/usr/local/libexec/mcpshka-broker", "lease-recovery-worker"}
	if args[0] == "recover-legacy-lease" {
		var p projectlease.RootSealedRecovery
		if err := json.Unmarshal([]byte(args[3]), &p); err != nil {
			return "", nil, err
		}
		command = append(command[:len(command)-2], append([]string{"--property=ReadWritePaths=" + p.GitCommonDir}, command[len(command)-2:]...)...)
	}

	return unit, append(command, args...), nil
}

func leaseRecoveryReadOnly(action string) bool {
	return strings.HasPrefix(action, "preview-") || strings.HasPrefix(action, "status-")
}

// PID1 grants these paths only to the fixed worker; the broker keeps its sandbox.
func runAdministrativeLeaseRecovery(ctx context.Context, args []string) ([]byte, error) {
	unit, command, err := legacyLeaseServiceArgs(args)
	if err != nil {
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
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", command...)
	out := &limitedBuffer{limit: 65536}
	cmd.Stdout = out
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if leaseRecoveryReadOnly(args[0]) {
			// A preview or status changes nothing: its refusal is a verified
			// HOLD, not an unknown effect (it read "do not replay" before).
			return nil, fmt.Errorf("lease recovery %s refused, nothing changed: %s", args[0], stderr.Bytes())
		}
		return nil, fmt.Errorf("lease recovery worker outcome UNKNOWN; inspect unit %s and current holder; do not replay: %s", unit, stderr.Bytes())
	}
	if out.truncated || !json.Valid(out.Bytes()) {
		return nil, errors.New("invalid lease recovery worker result: UNKNOWN")
	}
	return out.Bytes(), nil
}

// The worker revalidates the live grant under the existing lock before any CAS.
func RunLegacyLeaseWorker(args []string) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("root lease recovery worker required")
	}
	if err := validateLegacyLeaseRequest(args); err != nil {
		return nil, err
	}
	if args[0] == "preview-legacy-lease" || args[0] == "status-legacy-lease" || args[0] == "recover-legacy-lease" {
		root := args[3]
		if args[0] == "recover-legacy-lease" {
			var p projectlease.RootSealedRecovery
			if err := json.Unmarshal([]byte(root), &p); err != nil {
				return nil, err
			}
			root = p.Path
		}
		db, err := statestore.Active("/var/lib/mcpshka")
		if err != nil {
			return nil, err
		}
		if err := projectprofile.RecoveryAuthority(root, db); err != nil {
			return nil, err
		}
	}
	// Recovery checks deployment activity; a retired historical deployment
	// needs root attestation, and this worker is the root broker itself.
	deploy.UseRootHistoricalProof(runHistoricalFenceProof)
	var out any
	var err error
	switch args[0] {
	case "preview-legacy-lease":
		out, err = adminLegacyLeasePreview(args, time.Now())
	case "status-legacy-lease":
		out, err = adminLegacyLeaseStatus(args, time.Now())
	case "recover-legacy-lease":
		out, err = adminLegacyLeaseRecover(args, time.Now())
	case "preview-interactive-fence", "repair-interactive-fence":
		out, err = adminInteractiveFence(args, time.Now())
	case "preview-stale-permits", "reconcile-stale-permits":
		out, err = adminStalePermits(args, time.Now())
	case selfHealStalePermits:
		out, err = selfHealStalePermitsWorker()
	case "preview-stale-lease", "release-stale-lease":
		out, err = adminStaleLease(args, time.Now())
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// Only stale-record recovery admits nested scopes; fence repair stays immediate.
// The root worker checks every path component and the exact record ownership.
func staleLeaseRepository(path string) bool {
	if filepath.Clean(path) != path || !strings.HasPrefix(path, "/root/repos/") {
		return filepath.Clean(path) == path && projectlease.DeadScratchClone(path)
	}
	parts := strings.Split(strings.TrimPrefix(path, "/root/repos/"), "/")
	if len(parts) > 9 || !fenceRepairRepository("/root/repos/"+parts[0]) {
		return false
	}
	for _, part := range parts[1:] {
		if len(part) == 0 || len(part) > 128 || strings.Trim(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
			return false
		}
	}
	return true
}

func decodeStaleReleasePlan(raw string) (projectlease.StaleReleasePlan, error) {
	var p projectlease.StaleReleasePlan
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.DisallowUnknownFields()
	if len(raw) > 4096 || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || !staleLeaseRepository(p.Path) || p.LeaseID == "" || p.Owner == "" || !validHash(p.RecordSHA256) || p.LastHeartbeat.IsZero() {
		return p, errors.New("invalid stale lease release plan")
	}
	return p, nil
}

// adminStaleLease previews or releases a lease whose holder is provably
// gone, under the owner's active Face ID window (BUG-20261020-20).
func adminStaleLease(args []string, now time.Time) (any, error) {
	lock, _, err := adminLegacyLeaseSession(args, now)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	s := projectlease.New("/var/lib/mcpshka/leases", policy.Default())
	if args[0] == "preview-stale-lease" {
		return s.PreviewStaleRelease(args[3], now)
	}
	p, err := decodeStaleReleasePlan(args[3])
	if err != nil {
		return nil, err
	}
	return s.ReleaseStale(p, now)
}

// Linked worktrees may share a registered project's Git authority. Live Git
// identity is rechecked inside the root worker, never trusted from this shape.
func recoveryCommonDir(path, common string) bool {
	if common == path+"/.git" {
		return true
	}
	return strings.HasSuffix(common, "/.git") && staleLeaseRepository(strings.TrimSuffix(common, "/.git"))
}
