package deploy

// Helper validation, version probing and status files of deployments.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

func validateRootOwnedHelper(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("helper must be an executable regular non-symlink file")
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("helper must not be group/world writable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("helper must be owned by root")
	}
	return nil
}

// releaseProbeBudget covers the isolated probe's start under host load: at
// load 15-25 a 15 s budget timed out almost every first deploy attempt
// (BUG-20261022-37). The probe only reads a version, so one retry is safe.
const releaseProbeBudget = 45 * time.Second

func binaryVersion(parent context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, releaseProbeBudget)
	defer cancel()
	var out []byte
	var err error
	if brokerclient.Enabled() {
		out, err = brokerclient.Run(ctx, "release-probe", path, "version")
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() && parent.Err() == nil {
			retry, stop := context.WithTimeout(parent, releaseProbeBudget)
			out, err = brokerclient.Run(retry, "release-probe", path, "version")
			stop()
		}
	} else {
		if os.Geteuid() == 0 {
			return "", fmt.Errorf("root binary execution denied: isolated release probe required")
		}
		cmd := exec.CommandContext(ctx, path, "version")
		cmd.Env = policy.MinimalEnv("/var/lib/mcpshka/home")
		out, err = cmd.Output()
	}
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	const prefix = "mcpshka "
	if !strings.HasPrefix(line, prefix) || strings.Contains(line[len(prefix):], " ") {
		return "", fmt.Errorf("unexpected version output %q", line)
	}
	version := strings.TrimPrefix(line, prefix)
	if version == "" || strings.ContainsAny(version, "\t\r\n/") {
		return "", fmt.Errorf("invalid version %q", version)
	}
	return version, nil
}

func deploymentID() (string, error) {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixMilli(), hex.EncodeToString(buf[:])), nil
}

func validID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func writeStatus(path string, status Status) error {
	return writeStatusMode(path, status, false)
}

func writeStatusMode(path string, status Status, initial bool) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	db, err := statestore.Active(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return err
	}
	if db != nil {
		if initial {
			ok, err := db.PutIfAbsent("deploys/"+filepath.Base(path), data)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("deployment receipt already exists: HOLD")
			}
			return nil
		}
		return db.Put("deploys/"+filepath.Base(path), data)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".deploy-status-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if initial {
		if err := os.Link(tmpName, path); err != nil {
			return err
		}
	} else {
		if err := os.Rename(tmpName, path); err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = policy.MinimalEnv("/var/lib/mcpshka/home")
	return cmd.CombinedOutput()
}

func boundedText(data []byte, limit int) string {
	if len(data) <= limit {
		return strings.TrimSpace(string(data))
	}
	return strings.TrimSpace(string(data[:limit])) + "..."
}
