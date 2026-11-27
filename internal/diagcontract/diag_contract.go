package diagcontract

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	diagReleaseRoot = "/var/lib/chatops/projects/diag/releases"
	SmokeHelper     = "/usr/local/libexec/mcpshka-diag-smoke"
	ReleaseHelper   = "/usr/local/libexec/mcpshka-diag-release"
)

var diagVersionPattern = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,4}$`)
var diagCommitPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
var diagRunPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// These broker contracts accept NO arbitrary executable, destination,
// Docker command, config path, user-chosen release root, or runtime flags.
// The pinned root-owned helpers must separately verify SEALED manifest,
// hashes, no-follow opens, exclusivity and durable detached job ownership.
func ValidateSmoke(args []string) error {
	if len(args) != 3 {
		return errors.New("diag smoke requires fixed mode, version and checksum")
	}
	if args[0] != "native" && args[0] != "docker" && args[0] != "astra" {
		return fmt.Errorf("unsupported diag smoke mode %q", args[0])
	}
	if !validDiagVersion(args[1]) || !validDiagSHA(args[2]) {
		return errors.New("invalid diag smoke release identity")
	}
	return nil
}

// Upload/deploy may only reference artifacts in the pinned DIAG release
// staging tree. Only iCloud upload is authorized; no generic cloud transport,
// Docker execution, destination override, or sudo is exposed.
// A successful syntactic validation is NOT evidence of a sealed artifact.
func ValidateRelease(args []string) error {
	if len(args) == 2 && args[0] == "status" && diagRunPattern.MatchString(args[1]) {
		return nil
	}
	if len(args) != 4 {
		return errors.New("diag release requires action, version, artifact path, sha256")
	}
	if args[0] != "icloud-upload" && args[0] != "deploy" {
		return errors.New("unsupported diag release action")
	}
	version, path, sha := args[1], args[2], args[3]
	if !validDiagVersion(version) || !validDiagSHA(sha) || strings.ContainsAny(path, "\x00\n\r\t") {
		return errors.New("invalid diag release identity")
	}
	if filepath.Clean(path) != path {
		return errors.New("diag release path not canonical")
	}
	base := filepath.Base(filepath.Dir(path))
	prefix := ".staging-" + version + "-"
	if !strings.HasPrefix(base, prefix) || !diagCommitPattern.MatchString(strings.TrimPrefix(base, prefix)) {
		return errors.New("diag release staging identity not pinned")
	}
	if filepath.Dir(filepath.Dir(path)) != diagReleaseRoot {
		return errors.New("diag release outside pinned staging")
	}
	if args[0] == "icloud-upload" && filepath.Base(path) != "release-capsule-"+version+".tar.gz" {
		return errors.New("diag upload requires sealed capsule")
	}
	if args[0] == "deploy" && filepath.Base(path) != "diag-linux-arm64" {
		return errors.New("diag deploy requires the ARM64 executable")
	}
	return nil
}

// The privileged helper has a short, resource-capped scheduling window.
// Long Docker/iCloud-sync/deploy work MUST run under a separate durable, bounded
// worker owned by a DIAG run ID; broker calls must never wait for it.
func BoundedExecArgs(family, executable string, args []string) []string {
	digest := sha256.Sum256([]byte(family + "\x00" + strings.Join(args, "\x00")))
	argv := []string{
		"--quiet", "--collect", "--wait", "--pipe",
		fmt.Sprintf("--unit=mcpshka-diag-%x", digest[:12]),
		"--property=Type=exec", "--property=RuntimeMaxSec=25s",
		"--property=TimeoutStartSec=25s", "--property=MemoryMax=256M",
		"--property=CPUQuota=50%", "--property=TasksMax=16",
		"--property=UMask=0077", "--property=NoNewPrivileges=yes",
		"--property=ProtectSystem=full", "--property=ProtectHome=read-only",
		"--property=PrivateTmp=yes", "--property=PrivateDevices=yes",
		executable,
	}
	return append(argv, args...)
}

func validDiagVersion(v string) bool { return diagVersionPattern.MatchString(v) }
func validDiagSHA(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, r := range v {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
