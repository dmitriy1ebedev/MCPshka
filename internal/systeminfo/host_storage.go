package systeminfo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/policy"
)

const HostScanMaxEntries = 2048

type HostStoragePath struct {
	Path      string `json:"path"`
	Purpose   string `json:"purpose"`
	State     string `json:"state"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Entries   int    `json:"entries,omitempty"`
	Complete  bool   `json:"complete"`
	Reason    string `json:"reason,omitempty"`
}

type HostStorageInventory struct {
	Host              *StorageTrend     `json:"host,omitempty"`
	HostReason        string            `json:"host_reason,omitempty"`
	CapturedAt        time.Time         `json:"captured_at"`
	Paths             []HostStoragePath `json:"paths"`
	Git               string            `json:"git"`
	Leases            string            `json:"leases"`
	ActiveJobs        string            `json:"active_jobs"`
	ProcessReferences string            `json:"process_references"`
	DockerMounts      string            `json:"docker_mounts"`
}

func scanHostPath(ctx context.Context, p *policy.Policy, path, purpose string) HostStoragePath {
	result := HostStoragePath{Path: path, Purpose: purpose, State: "unknown"}
	real, e := p.ResolveExisting(path)
	if e != nil {
		if os.IsNotExist(e) {
			if _, e2 := p.ResolveForWrite(path); e2 == nil {
				result.State = "absent"
				result.Complete = true
				result.Reason = "path does not exist"
				return result
			}
		}
		result.Reason = "permission_denied_or_outside_allowed_roots; use a privileged inventory provider, not unrestricted root"
		return result
	}
	st, e := os.Lstat(real)
	if e != nil {
		result.Reason = "lstat_failed"
		return result
	}
	if st.Mode().IsRegular() {
		result.State = "known"
		result.Complete = true
		result.SizeBytes = st.Size()
		result.Entries = 1
		return result
	}
	if !st.IsDir() {
		result.Reason = "special_object; not eligible for cleanup"
		return result
	}
	// Device IDs alone do not catch same-device bind mounts. The current
	// namespace mount table is necessary but not sufficient evidence: Docker
	// may have a distinct mount namespace, therefore cleanup still HOLDS.
	if ctx.Err() != nil {
		result.State, result.Reason = "partial", "cancelled"
		return result
	}
	mountInfo, mountErr := readHostMountInfo()
	if mountErr != nil {
		result.State, result.Reason = "partial", "mount_evidence_unavailable"
		return result
	}
	nested, mountErr := hasNestedHostMount(real, mountInfo)
	if mountErr != nil {
		result.State, result.Reason = "partial", "mount_evidence_invalid"
		return result
	}
	if nested {
		result.State, result.Reason = "partial", "filesystem_mount_boundary"
		return result
	}
	rootDevice := uint64(0)
	if raw, ok := st.Sys().(*syscall.Stat_t); ok {
		rootDevice = uint64(raw.Dev)
	}
	count := 0
	var size int64
	partial := ""
	e = filepath.WalkDir(real, func(name string, de fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			partial = "cancelled"
			return fs.SkipAll
		}
		if walkErr != nil {
			partial = "unreadable_child"
			return fs.SkipAll
		}
		if name == real {
			return nil
		}
		count++
		if count > HostScanMaxEntries {
			partial = "entry_budget_exceeded"
			return fs.SkipAll
		}
		info, err := os.Lstat(name)
		if err != nil {
			partial = "concurrent_mutation_or_stat_failed"
			return fs.SkipAll
		}
		if raw, ok := info.Sys().(*syscall.Stat_t); ok && rootDevice != 0 && uint64(raw.Dev) != rootDevice {
			partial = "filesystem_mount_boundary"
			return fs.SkipAll
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.Mode().IsRegular() {
			if info.Size() < 0 || size > 1<<60-info.Size() {
				partial = "size_overflow"
				return fs.SkipAll
			}
			size += info.Size()
		} else if !info.IsDir() {
			partial = "special_object_found"
			return fs.SkipAll
		}
		return nil
	})
	result.SizeBytes = size
	result.Entries = count
	if e != nil && partial == "" {
		partial = "walk_failed"
	}
	if partial != "" {
		result.State = "partial"
		result.Reason = partial
		return result
	}
	result.State = "known"
	result.Complete = true
	return result
}
func InventoryHostStorage(ctx context.Context, p *policy.Policy, root string) (HostStorageInventory, error) {
	if ctx == nil || p == nil {
		return HostStorageInventory{}, errors.New("context and policy required")
	}
	project, e := p.CheckWorkdir(root)
	if e != nil {
		return HostStorageInventory{}, fmt.Errorf("project path invalid: %w; choose an allowed workdir", e)
	}
	specs := [][2]string{{project, "repository"}, {filepath.Join(project, "bin"), "build_artifacts"},
		{"/usr/local/bin/mcpshka", "production_binary"}, {"/usr/local/bin/mcpshka.previous", "rollback_binary"},
		{"/var/lib/mcpshka", "service_data"}, {"/var/lib/mcpshka-dev", "build_cache"},
		{"/opt/homeassistant/config", "user_data"}, {"/var/lib/chatops", "user_data"}}
	out := HostStorageInventory{CapturedAt: time.Now().UTC(), Paths: make([]HostStoragePath, 0, len(specs)),
		Git: "unknown: Git status not reconciled", Leases: "unknown: writer lease state not reconciled",
		ActiveJobs: "unknown: durable jobs not reconciled", ProcessReferences: "unknown: process descriptors not inspected",
		DockerMounts: "unknown: Docker bind mounts not inspected"}
	if trend, e := ReadStorageTrend(); e == nil {
		out.Host = &trend
	} else {
		out.HostReason = e.Error()
	}
	for _, x := range specs {
		out.Paths = append(out.Paths, scanHostPath(ctx, p, x[0], x[1]))
	}
	return out, nil
}

type HostHousekeepingItem struct {
	Path         string `json:"path"`
	Action       string `json:"action"`
	Reason       string `json:"reason"`
	ReclaimBytes int64  `json:"reclaim_bytes"`
}
type HostHousekeepingPlan struct {
	State                string                 `json:"state"`
	Items                []HostHousekeepingItem `json:"items"`
	Blockers             []string               `json:"blockers"`
	VerifiedReclaimBytes int64                  `json:"verified_reclaim_bytes"`
}

// Docker environment ordering is not semantically significant. Duplicated
// keys are rejected because choosing the first or last value is ambiguous.
func DockerEnvironmentEqual(a, b []string) bool {
	normalize := func(raw []string) (map[string]string, bool) {
		result := make(map[string]string, len(raw))
		for _, pair := range raw {
			key, value, ok := strings.Cut(pair, "=")
			if !ok || key == "" {
				return nil, false
			}
			if _, duplicate := result[key]; duplicate {
				return nil, false
			}
			result[key] = value
		}
		return result, true
	}
	x, ok := normalize(a)
	if !ok {
		return false
	}
	y, ok := normalize(b)
	if !ok || len(x) != len(y) {
		return false
	}
	for key, value := range x {
		if other, exists := y[key]; !exists || other != value {
			return false
		}
	}
	return true
}

func PlanHostHousekeeping(inv HostStorageInventory) HostHousekeepingPlan {
	plan := HostHousekeepingPlan{State: "HOLD", Items: make([]HostHousekeepingItem, 0, len(inv.Paths)), Blockers: []string{}}
	for _, x := range inv.Paths {
		action, reason := "keep", "preserve production, rollback, source and user data"
		if x.Purpose == "build_cache" || x.Purpose == "build_artifacts" {
			action = "review"
			reason = "requires exact snapshot, process/mount/lease exclusion and production pins"
		}
		if !x.Complete || x.State != "known" {
			action = "keep"
			reason = "insufficient evidence: " + x.Reason
			plan.Blockers = append(plan.Blockers, x.Path+": "+x.Reason)
		}
		plan.Items = append(plan.Items, HostHousekeepingItem{Path: x.Path, Action: action, Reason: reason})
	}
	for _, v := range []string{inv.Git, inv.Leases, inv.ActiveJobs, inv.ProcessReferences, inv.DockerMounts} {
		if strings.HasPrefix(v, "unknown") {
			plan.Blockers = append(plan.Blockers, v)
		}
	}
	return plan
}

const hostMountInfoMaxBytes = 1 << 20
const hostMountInfoMaxEntries = 8192

// readHostMountInfo is bounded. A failed or partial namespace inventory is
// UNKNOWN, not proof that a path contains no mounts.
func readHostMountInfo() ([]byte, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, hostMountInfoMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > hostMountInfoMaxBytes {
		return nil, errors.New("mountinfo empty or over budget")
	}
	return data, nil
}

// hasNestedHostMount refuses even bind mounts sharing the parent's device.
// This checks only the caller's namespace. A clean result never certifies
// absence of Docker mount references or other namespaces.
func hasNestedHostMount(root string, data []byte) (bool, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || len(data) == 0 || len(data) > hostMountInfoMaxBytes {
		return false, errors.New("invalid mount evidence")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) > hostMountInfoMaxEntries {
		return false, errors.New("mount table exceeds entry budget")
	}
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) < 10 {
			return false, errors.New("incomplete mountinfo record")
		}
		separator := false
		for _, part := range parts[6:] {
			if part == "-" {
				separator = true
				break
			}
		}
		if !separator {
			return false, errors.New("mountinfo separator missing")
		}
		point, err := decodeMountInfoPath(parts[4])
		if err != nil || !filepath.IsAbs(point) || filepath.Clean(point) != point {
			return false, errors.New("invalid mountpoint in evidence")
		}
		rel, err := filepath.Rel(root, point)
		if err != nil {
			return false, err
		}
		if rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true, nil
		}
	}
	return false, nil
}

func decodeMountInfoPath(raw string) (string, error) {
	var result strings.Builder
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			result.WriteByte(raw[i])
			i++
			continue
		}
		if i+4 > len(raw) {
			return "", errors.New("truncated mountinfo escape")
		}
		value, err := strconv.ParseUint(raw[i+1:i+4], 8, 8)
		if err != nil || value == 0 {
			return "", errors.New("invalid mountinfo escape")
		}
		result.WriteByte(byte(value))
		i += 4
	}
	return result.String(), nil
}
