package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/audit"
)

// ArtifactGC keeps authoritative SQLite metadata, stdout/stderr receipts and
// recovery evidence. It only removes disposable Go compiler scratch trees.
const (
	successfulArtifactRetention   = 72 * time.Hour
	unsuccessfulArtifactRetention = 96 * time.Hour
	maxArtifactGCItems            = 8
	maxArtifactGCBytes            = 512 << 20
)

var artifactGoBuildName = regexp.MustCompile(`^go-build[0-9]+$`)

type ArtifactGCItem struct {
	JobID   string `json:"job_id"`
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	AgeDays int    `json:"age_days"`
	State   State  `json:"state"`
	info    os.FileInfo
}

type ArtifactGCReport struct {
	State         string           `json:"state"`
	Mode          string           `json:"mode"`
	PlanSHA256    string           `json:"plan_sha256"`
	Eligible      int              `json:"eligible"`
	EligibleBytes int64            `json:"eligible_bytes"`
	Deleted       int              `json:"deleted"`
	DeletedBytes  int64            `json:"deleted_bytes"`
	Skipped       int              `json:"skipped"`
	Items         []ArtifactGCItem `json:"items"`
	Reason        string           `json:"reason,omitempty"`
}

// ArtifactGC returns a read-only, SHA-pinned preview unless apply is true.
// expectedPlan is mandatory for an interactive apply; only the daemon's
// internal maintenance tick may omit it (it holds the manager lock and
// computes its plan in the same call). An interrupted job is never terminal.
func (m *Manager) ArtifactGC(now time.Time, apply bool, expectedPlan string) (ArtifactGCReport, error) {
	out := ArtifactGCReport{State: "COMPLETE", Mode: "preview"}
	if apply {
		out.Mode = "apply"
	}
	if now.IsZero() {
		return out, errors.New("artifact gc requires current time")
	}
	// Lease references may consult another durable service: never call the
	// callback under the manager lock or risk deadlocking job admissions.
	m.mu.RLock()
	references := m.reapReferences
	m.mu.RUnlock()
	// The reaper may tick before the workflow coordinator installs its
	// authoritative reference provider; missing authority must fail closed.
	if references == nil {
		out.State, out.Reason = "HOLD", "lease/job reference authority not configured"
		return out, nil
	}
	refs, err := references()
	if err != nil || refs == nil {
		out.State, out.Reason = "HOLD", "lease/job reference authority unavailable"
		return out, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leaseGuard == nil {
		out.State, out.Reason = "HOLD", "project lease authority not configured"
		return out, nil
	}

	ids := make([]string, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		j := m.jobs[id]
		if j == nil || !isTerminal(j.State) || j.FinishedAt.IsZero() || j.FinishedAt.After(now) ||
			refs[id] || strings.HasPrefix(id, "workflow-") ||
			j.Error == "mcpshka restarted while job was active" ||
			(j.External != nil && !j.ResourcesReleased) {
			continue
		}
		// The scratch lives in the job's own directory: a removed worktree
		// (the common case for timed-out tests in wt/…) does not protect it,
		// it only means there is no project lease left to consult.
		vanished := workdirVanished(j.Workdir)
		ttl := unsuccessfulArtifactRetention
		if j.State == Done {
			ttl = successfulArtifactRetention
		}
		age := now.Sub(j.FinishedAt)
		if age < ttl {
			continue
		}
		if !vanished && m.leaseGuard != nil && m.leaseGuard(j.Workdir, "") != nil {
			continue
		}
		if j.PID > 0 || j.ScopeUnit != "" {
			gone, err := unknownGroupGone(j)
			if err != nil || !gone {
				continue
			}
		}
		if !isSafeGCName(id) {
			continue
		}
		jobDir := filepath.Join(m.root, id)
		tmp := filepath.Join(jobDir, "tmp")
		if !gcSafeDirectory(jobDir) || !gcSafeDirectory(tmp) {
			continue
		}
		entries, err := os.ReadDir(tmp)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !artifactGoBuildName.MatchString(entry.Name()) || entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			path := filepath.Join(tmp, entry.Name())
			if !gcSafeDirectory(path) {
				continue
			}
			info, err := os.Lstat(path)
			if err != nil {
				continue
			}
			bytes, err := gcBoundedTreeBytes(path, info)
			if err != nil {
				continue
			}
			out.Items = append(out.Items, ArtifactGCItem{JobID: id, Path: filepath.ToSlash(filepath.Join(id, "tmp", entry.Name())), Bytes: bytes, AgeDays: int(age / (24 * time.Hour)), State: j.State, info: info})
			out.Eligible++
			out.EligibleBytes += bytes
		}
	}
	digest := sha256.New()
	for _, item := range out.Items {
		fmt.Fprintf(digest, "%s|%s|%d|%d|%t\n", item.JobID, item.Path, item.Bytes, item.info.ModTime().UnixNano(), item.State == Done)
	}
	out.PlanSHA256 = hex.EncodeToString(digest.Sum(nil))
	if !apply {
		return out, nil
	}
	if expectedPlan != "" && expectedPlan != out.PlanSHA256 {
		out.State, out.Reason = "HOLD", "candidate plan changed; preview again"
		return out, nil
	}
	// Keep every destructive path confined to the job storage root even if
	// another process swaps a checked intermediate directory for a symlink.
	// Root.RemoveAll refuses traversal outside that directory.
	storage, err := os.OpenRoot(m.root)
	if err != nil {
		out.State, out.Reason = "HOLD", "job storage root unavailable"
		return out, nil
	}
	defer storage.Close()
	var spent int64
	for _, item := range out.Items {
		if out.Deleted >= maxArtifactGCItems || spent+item.Bytes > maxArtifactGCBytes {
			out.State, out.Reason = "PARTIAL", "bounded batch: retry on the next maintenance tick"
			break
		}
		path := filepath.Join(m.root, filepath.FromSlash(item.Path))
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(item.info, current) || item.info.ModTime() != current.ModTime() || item.info.Size() != current.Size() || !gcSafeDirectory(filepath.Dir(path)) {
			out.Skipped++
			continue
		}
		// Refuse symlinks, hardlinks, mounts, special files or size drift.
		size, err := gcBoundedTreeBytes(path, current)
		if err != nil || size != item.Bytes {
			out.Skipped++
			continue
		}
		if err := storage.RemoveAll(filepath.FromSlash(item.Path)); err != nil {
			out.Skipped++
			continue
		}
		spent += item.Bytes
		out.Deleted++
		out.DeletedBytes += item.Bytes
		m.log(audit.Event{Type: "job_artifact_gc", JobID: item.JobID, Result: "removed disposable compiler scratch; receipt retained"})
	}
	if out.Skipped > 0 && out.State == "COMPLETE" {
		out.State = "PARTIAL"
		out.Reason = "changed targets preserved"
	}
	return out, nil
}

func isSafeGCName(id string) bool {
	if len(id) < 3 || len(id) > 100 {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func gcSafeDirectory(path string) bool {
	s, err := os.Lstat(path)
	return err == nil && s.IsDir() && s.Mode()&os.ModeSymlink == 0
}

func gcBoundedTreeBytes(path string, root os.FileInfo) (int64, error) {
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return 0, errors.New("unsafe gc target")
	}
	stat, ok := root.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("unsupported inode identity")
	}
	var total int64
	var entries int
	err := filepath.WalkDir(path, func(child string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 300000 {
			return errors.New("gc tree too large")
		}
		st, err := os.Lstat(child)
		if err != nil {
			return err
		}
		ino, ok := st.Sys().(*syscall.Stat_t)
		if !ok || ino.Dev != stat.Dev || st.Mode()&os.ModeSymlink != 0 || st.Mode()&os.ModeType != 0 && !st.IsDir() {
			return errors.New("gc tree has link, mount or special file")
		}
		if st.Mode().IsRegular() {
			if ino.Nlink != 1 {
				return errors.New("gc hardlink refused")
			}
			total += st.Size()
			if total > maxArtifactGCBytes {
				return errors.New("gc tree exceeds per-run budget")
			}
		}
		return nil
	})
	return total, err
}
