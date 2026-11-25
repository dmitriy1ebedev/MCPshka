package systeminfo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"mcpshka/internal/policy"
)

type HostRelocateDependency struct {
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}
type HostRelocatePlan struct {
	Source         string                   `json:"source"`
	Destination    string                   `json:"destination"`
	SourceSHA256   string                   `json:"source_sha256,omitempty"`
	SourceDevice   uint64                   `json:"source_device,omitempty"`
	SourceInode    uint64                   `json:"source_inode,omitempty"`
	SourceSize     int64                    `json:"source_size,omitempty"`
	SameFilesystem bool                     `json:"same_filesystem"`
	State          string                   `json:"state"`
	Dependencies   []HostRelocateDependency `json:"dependencies"`
	Blockers       []string                 `json:"blockers"`
	NextStep       string                   `json:"next_step"`
}

func PlanHostRelocate(ctx context.Context, p *policy.Policy, source, destination string) (HostRelocatePlan, error) {
	result := HostRelocatePlan{Source: source, Destination: destination, State: "HOLD"}
	if ctx == nil || p == nil {
		return result, errors.New("context and filesystem policy required")
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || filepath.Clean(source) != source || filepath.Clean(destination) != destination {
		return result, errors.New("canonical absolute source and destination paths required")
	}
	if source == destination {
		return result, errors.New("source and destination are identical")
	}
	resolvedSource, aliasError := filepath.EvalSymlinks(source)
	if aliasError != nil {
		return result, fmt.Errorf("source path cannot be resolved: %w", aliasError)
	}
	if resolvedSource != source {
		return result, errors.New("source path is a symlink alias; use the canonical path")
	}
	real, err := p.ResolveExisting(source)
	if err != nil {
		return result, fmt.Errorf("source inaccessible: %w; inspect using a reviewed read-only privileged inventory provider", err)
	}
	dest, err := p.ResolveForWrite(destination)
	if err != nil {
		return result, fmt.Errorf("destination outside approved scope: %w; use a policy-approved target directory", err)
	}
	if real == dest {
		return result, errors.New("source aliases destination")
	}
	rel, err := filepath.Rel(real, dest)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return result, errors.New("destination nested within source: relocation would recurse")
	}
	src, err := os.Lstat(real)
	if err != nil {
		return result, err
	}
	if src.Mode()&os.ModeSymlink != 0 || (!src.IsDir() && !src.Mode().IsRegular()) {
		return result, errors.New("source symlink or special object must be reconciled separately")
	}
	if _, err := os.Lstat(dest); err == nil {
		return result, errors.New("destination already exists; no overwrite permitted")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	parent, err := os.Stat(filepath.Dir(dest))
	if err != nil {
		return result, err
	}
	if v, ok := src.Sys().(*syscall.Stat_t); ok {
		result.SourceDevice = uint64(v.Dev)
		result.SourceInode = v.Ino
	}
	if v, ok := parent.Sys().(*syscall.Stat_t); ok && result.SourceDevice != 0 {
		result.SameFilesystem = uint64(v.Dev) == result.SourceDevice
	}
	result.SourceSize = src.Size()
	if src.Mode().IsRegular() && src.Size() <= 16<<20 {
		f, err := os.Open(real)
		if err != nil {
			return result, err
		}
		h := sha256.New()
		n, readErr := io.CopyN(h, f, src.Size())
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || n != src.Size() {
			return result, errors.New("source changed during digest")
		}
		fresh, err := os.Lstat(real)
		if err != nil || !os.SameFile(src, fresh) || fresh.Size() != src.Size() {
			return result, errors.New("source changed during inventory: HOLD")
		}
		result.SourceSHA256 = hex.EncodeToString(h.Sum(nil))
	}
	for _, name := range []string{"systemd_units", "docker_mounts_and_env", "installer_paths", "symlink_references", "python_environments", "git_worktrees", "open_process_fds", "jobs_and_leases", "backup_and_rollback"} {
		result.Dependencies = append(result.Dependencies, HostRelocateDependency{Kind: name, State: "UNKNOWN", Reason: "privileged dependency evidence not yet collected"})
		result.Blockers = append(result.Blockers, name+": evidence required")
	}
	if !result.SameFilesystem {
		result.Blockers = append(result.Blockers, "cross-filesystem transaction requires copy/fsync and retained source rollback")
	}
	if src.IsDir() {
		result.Blockers = append(result.Blockers, "directory needs bounded, immutable full-tree manifest")
	}
	result.NextStep = "collect root-broker read-only dependency graph and obtain a pinned, resumable relocation journal; do not move the source yet"
	return result, nil
}
