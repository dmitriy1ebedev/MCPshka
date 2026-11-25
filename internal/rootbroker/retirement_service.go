package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func validateRetirementRequest(args []string) error {
	if len(args) != 7 || args[0] != "retire-deployment" || !validHex(args[1], 32) || !safeOwner(args[2]) || args[2] == "local-stdio" || !adminDeploymentID.MatchString(args[3]) || !validHash(args[4]) || !validHash(args[5]) || ValidateRetirementReasonV1(args[6]) != nil {
		return errors.New("invalid fixed deployment retirement")
	}
	return nil
}

// Keep these original protocol rules immutable: they prove rejection before any worker.
func ValidateRetirementReasonV1(reason string) error {
	if len(reason) > 200 || strings.TrimSpace(reason) == "" || strings.ContainsAny(reason, "\r\n\x00") || strings.HasPrefix(reason, "-") {
		return errors.New("retirement reason requires 1..200 bytes, no CR/LF/NUL or leading dash")
	}
	return nil
}

func retirementServiceArgs(args []string) (string, []string, error) {
	if err := validateRetirementRequest(args); err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", nil, err
	}
	unit := "mcpshka-retire-" + shaHex(raw)[:32]
	command := []string{"--unit=" + unit, "--collect", "--wait", "--pipe", "--quiet",
		"--property=Type=exec", "--property=User=root", "--property=Group=root", "--property=UMask=0077",
		"--property=RuntimeMaxSec=45s", "--property=MemoryMax=128M", "--property=CPUQuota=50%", "--property=TasksMax=16",
		"--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes", "--property=PrivateDevices=yes",
		"--property=PrivateNetwork=yes", "--property=ProtectSystem=strict", "--property=ProtectHome=yes",
		"--property=ReadOnlyPaths=/var/lib/mcpshka/state.db",
		// SQLite may create WAL sidecars; the main database stays a read-only mount.
		"--property=ReadWritePaths=/var/lib/mcpshka /var/lib/mcpshka-release-gates /var/lib/mcpshka-executor /run/mcpshka-self-deploy.lock",
		"--property=StateDirectory=mcpshka-deploy-fences", "--property=StateDirectoryMode=0755",
		"--property=CapabilityBoundingSet=CAP_DAC_READ_SEARCH CAP_SETUID CAP_SETGID CAP_SYS_PTRACE",
		"--property=AmbientCapabilities=CAP_SETUID",
		"--property=RestrictAddressFamilies=AF_UNIX", "--property=RestrictNamespaces=yes",
		"--property=ProtectKernelTunables=yes", "--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes",
		"/usr/local/libexec/mcpshka-broker", "retirement-worker"}
	command = append(command, args...)
	return unit, command, nil
}

func RunRetirementWorker(ctx context.Context, args []string) ([]byte, error) {
	if err := validateRetirementRequest(args); err != nil {
		return nil, err
	}
	if os.Geteuid() != 0 {
		return nil, errors.New("authorized root retirement worker required")
	}
	if args[3] == legacyFenceID {
		return sealLegacyRetirement(ctx, args)
	}
	if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-self-deploy"); err != nil {
		return nil, err
	}
	// Keep the waiting launcher separate from the deployment gate's process scan.
	command := []string{"-I", "-S", "/usr/local/libexec/mcpshka-self-deploy", "retire-deployment",
		"--session-id", args[1], "--owner", args[2], "--deployment-id", args[3],
		"--expected-receipt-sha256", args[4], "--expected-current-sha256", args[5], "--reason", args[6]}
	raw, err := exec.CommandContext(ctx, "/usr/bin/python3", command...).Output()
	if err != nil {
		return nil, errors.New("retirement worker UNKNOWN; inspect deployment fence")
	}
	if len(raw) > 65536 || !json.Valid(raw) {
		return nil, errors.New("retirement worker result invalid: UNKNOWN")
	}
	return raw, nil
}

func runAdministrativeRetirement(ctx context.Context, args []string) ([]byte, error) {
	unit, command, err := retirementServiceArgs(args)
	if err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-self-deploy"); err != nil {
		return nil, err
	}
	if err := validateRootOwnedExecutable("/usr/local/libexec/mcpshka-broker"); err != nil {
		return nil, err
	}
	lock, err := openAdminSessionLock(taskRoot)
	if err != nil {
		return nil, err
	}
	current, err := readAdminSession(taskRoot, time.Now())
	if err == nil {
		err = requireAdminSession(current, args[1], args[2], current.Generation, time.Now())
	}
	lock.Close()
	if err != nil {
		return nil, err
	}
	// PID1 starts the fixed sandbox; the broker keeps its existing capabilities.
	// The gate rechecks the grant under its lock before any deployment effect.
	raw, err := exec.CommandContext(ctx, "/usr/bin/systemd-run", command...).Output()
	if err != nil {
		return nil, fmt.Errorf("retirement UNKNOWN; inspect unit %s and deployment; do not replay", unit)
	}
	if len(raw) > 65536 || !json.Valid(raw) {
		return nil, errors.New("retirement result invalid: UNKNOWN")
	}
	return raw, nil
}
