package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func sameCandidateMount(parent, child *os.File) error {
	var a, b unix.Statx_t
	for _, p := range []struct {
		f  *os.File
		st *unix.Statx_t
	}{{parent, &a}, {child, &b}} {
		if err := unix.Statx(int(p.f.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, p.st); err != nil || p.st.Mask&unix.STATX_MNT_ID == 0 {
			return fmt.Errorf("candidate mount identity unavailable: HOLD")
		}
	}
	if a.Mnt_id != b.Mnt_id {
		return fmt.Errorf("candidate crosses mount boundary: HOLD")
	}
	return nil
}

type candidateIdentity struct {
	Name                 string
	Device, Inode, Links uint64
	UID                  uint32
	Mode                 os.FileMode
	Size, Mtime, Ctime   int64
	SHA256               string
}

func candidateMetadata(name string, info os.FileInfo) (candidateIdentity, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return candidateIdentity{}, fmt.Errorf("candidate identity unavailable: HOLD")
	}
	return candidateIdentity{Name: name, Device: uint64(st.Dev), Inode: st.Ino, Links: uint64(st.Nlink), UID: st.Uid, Mode: info.Mode(), Size: info.Size(), Mtime: info.ModTime().UnixNano(), Ctime: st.Ctim.Sec*1e9 + st.Ctim.Nsec}, nil
}

func snapshotCandidate(ctx context.Context, name string, f *os.File, budget *int64) (candidateIdentity, error) {
	before, err := f.Stat()
	if err != nil {
		return candidateIdentity{}, err
	}
	identity, err := candidateMetadata(name, before)
	if err != nil {
		return candidateIdentity{}, err
	}
	if !before.Mode().IsRegular() || identity.Links != 1 || identity.Size < 0 || identity.Size > 128<<20 || identity.Size > *budget {
		return candidateIdentity{}, fmt.Errorf("unsafe candidate or snapshot budget exceeded: HOLD")
	}
	h := sha256.New()
	buffer := make([]byte, 32<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return candidateIdentity{}, err
		}
		n, err := f.Read(buffer)
		size += int64(n)
		if size > identity.Size {
			return candidateIdentity{}, fmt.Errorf("candidate grew during snapshot: HOLD")
		}
		_, _ = h.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return candidateIdentity{}, err
		}
	}
	after, err := f.Stat()
	if err != nil {
		return candidateIdentity{}, err
	}
	current, err := candidateMetadata(name, after)
	if err != nil || current != identity || size != identity.Size {
		return candidateIdentity{}, fmt.Errorf("candidate changed during snapshot: HOLD")
	}
	*budget -= size
	identity.SHA256 = hex.EncodeToString(h.Sum(nil))
	return identity, nil
}

type candidatePin struct {
	identity candidateIdentity
	file     *os.File
}

func (p candidatePin) revalidate(info os.FileInfo, current *os.File) error {
	expected := p.identity
	expected.SHA256 = ""
	observed, err := candidateMetadata(expected.Name, info)
	if err != nil || observed != expected {
		return fmt.Errorf("candidate path changed after snapshot: HOLD")
	}
	if p.file != nil {
		st, err := p.file.Stat()
		if err != nil {
			return err
		}
		observed, err = candidateMetadata(expected.Name, st)
		if err != nil || observed != expected {
			return fmt.Errorf("candidate descriptor changed after snapshot: HOLD")
		}
		if current == nil {
			return fmt.Errorf("candidate mount unverified: HOLD")
		}
		if err := sameCandidateMount(p.file, current); err != nil {
			return err
		}
		// Timestamps are coarse on some filesystems (a CI overlay missed a
		// same-size rewrite within one tick): the content must still match.
		if p.identity.SHA256 != "" {
			sum, err := contentSHA256(current, info.Size())
			if err != nil || sum != p.identity.SHA256 {
				return fmt.Errorf("candidate content changed after snapshot: HOLD")
			}
		}
	}
	return nil
}

func contentSHA256(f *os.File, size int64) (string, error) {
	if size < 0 || size > 512<<20 {
		return "", fmt.Errorf("candidate size unverified: HOLD")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.NewSectionReader(f, 0, size+1))
	if err != nil || n != size {
		return "", fmt.Errorf("candidate read unverified: HOLD")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func protectedCandidate(ctx context.Context, path string, budget *int64) (candidatePin, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return candidatePin{}, fmt.Errorf("protected binary unavailable: HOLD: %w", err)
	}
	identity, err := snapshotCandidate(ctx, filepath.Base(path), f, budget)
	if err != nil {
		_ = f.Close()
		return candidatePin{}, err
	}
	return candidatePin{identity, f}, nil
}

func (s *Service) candidatePlan(ctx context.Context, path string) (CandidateCleanup, error) {
	root, err := s.policy.ResolveExisting(path)
	if err != nil {
		return CandidateCleanup{}, err
	}
	if filepath.Clean(root) != s.repoRoot {
		return CandidateCleanup{}, fmt.Errorf("candidate cleanup currently supports only %s", s.repoRoot)
	}
	budget := int64(2 << 30)
	installed, err := protectedCandidate(ctx, s.target, &budget)
	if err != nil {
		return CandidateCleanup{}, err
	}
	defer installed.file.Close()
	rollback, err := protectedCandidate(ctx, s.target+".previous", &budget)
	if err != nil {
		return CandidateCleanup{}, err
	}
	defer rollback.file.Close()
	version, err := binaryVersion(ctx, s.target)
	if err != nil {
		return CandidateCleanup{}, err
	}
	previous, err := binaryVersion(ctx, s.target+".previous")
	if err != nil {
		return CandidateCleanup{}, err
	}
	brokers, err := installedBrokerHashes(ctx, &budget)
	if err != nil {
		return CandidateCleanup{}, err
	}
	bin := filepath.Join(root, "bin")
	info, err := os.Lstat(bin)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return CandidateCleanup{}, fmt.Errorf("candidate parent unavailable or aliased: HOLD")
	}
	parent, err := candidateMetadata("bin", info)
	if err != nil {
		return CandidateCleanup{}, err
	}
	directory, err := os.OpenRoot(bin)
	if err != nil {
		return CandidateCleanup{}, err
	}
	defer directory.Close()
	f, err := directory.Open(".")
	if err != nil {
		return CandidateCleanup{}, err
	}
	defer f.Close()
	pinned, err := f.Stat()
	if err != nil || !os.SameFile(info, pinned) {
		return CandidateCleanup{}, fmt.Errorf("candidate parent changed: HOLD")
	}
	repository, err := os.OpenFile(root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return CandidateCleanup{}, err
	}
	defer repository.Close()
	if err := sameCandidateMount(repository, f); err != nil {
		return CandidateCleanup{}, err
	}
	entries, err := f.ReadDir(4097)
	if err != nil && err != io.EOF {
		return CandidateCleanup{}, err
	}
	if len(entries) > 4096 {
		return CandidateCleanup{}, fmt.Errorf("candidate enumeration budget exceeded: HOLD")
	}
	out := CandidateCleanup{Root: root, CurrentVersion: version, RollbackVersion: previous, Keep: []string{}, Remove: []string{}}
	released := releaseProvenance(directory, entries)
	items := []candidateIdentity{}
	pins := []candidatePin{}
	defer func() {
		for _, p := range pins {
			if p.file != nil {
				_ = p.file.Close()
			}
		}
	}()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return CandidateCleanup{}, err
		}
		name := entry.Name()
		if !(strings.HasPrefix(name, "candidate-state-") || strings.HasPrefix(name, "mcpshka-") && strings.HasSuffix(name, ".candidate")) {
			continue
		}
		if len(items) >= 64 {
			return CandidateCleanup{}, fmt.Errorf("candidate entry budget exceeded: HOLD")
		}
		st, err := directory.Lstat(name)
		if err != nil {
			return CandidateCleanup{}, err
		}
		item, err := candidateMetadata(name, st)
		if err != nil {
			return CandidateCleanup{}, err
		}
		protected := false
		pin := candidatePin{identity: item}
		if st.Mode().IsRegular() && item.Links == 1 {
			child, err := directory.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if errors.Is(err, os.ErrPermission) {
				// A root-staged candidate (0700) cannot be hashed: keep it, the
				// rest of the plan stands (BUG-20261020-14).
				pins = append(pins, pin)
				items = append(items, item)
				out.Keep = append(out.Keep, name)
				out.Unknown = append(out.Unknown, name)
				continue
			}
			if err != nil {
				return CandidateCleanup{}, err
			}
			if err := sameCandidateMount(f, child); err != nil {
				_ = child.Close()
				return CandidateCleanup{}, err
			}
			observed, err := snapshotCandidate(ctx, name, child, &budget)
			if err != nil {
				_ = child.Close()
				return CandidateCleanup{}, err
			}
			if observed.Device != item.Device || observed.Inode != item.Inode || observed.Ctime != item.Ctime {
				_ = child.Close()
				return CandidateCleanup{}, fmt.Errorf("candidate replaced during snapshot: HOLD")
			}
			item = observed
			pin = candidatePin{item, child}
			protected = item.SHA256 == installed.identity.SHA256 || item.SHA256 == rollback.identity.SHA256 || brokers[item.SHA256]
		}
		pins = append(pins, pin)
		items = append(items, item)
		if v, ok := released[item.SHA256]; ok && !protected && item.SHA256 != "" && versionBefore(v, previous) {
			// MCPshka's own release receipt names this exact content for a
			// release older than the rollback: it can never be deployed again.
			out.Remove = append(out.Remove, name)
			out.RemoveBytes += item.Size
			out.removeItems = append(out.removeItems, item)
			continue
		}
		out.Keep = append(out.Keep, name)
		if !protected {
			out.Unknown = append(out.Unknown, name)
		}
	}
	// Recheck every hashed descriptor and public path before accepting the plan.
	for _, pin := range pins {
		st, err := directory.Lstat(pin.identity.Name)
		if err != nil {
			return CandidateCleanup{}, err
		}
		var current *os.File
		if pin.file != nil {
			current, err = directory.OpenFile(pin.identity.Name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return CandidateCleanup{}, err
			}
		}
		err = pin.revalidate(st, current)
		if current != nil {
			_ = current.Close()
		}
		if err != nil {
			return CandidateCleanup{}, err
		}
	}
	for _, protected := range []struct {
		path string
		pin  candidatePin
	}{{s.target, installed}, {s.target + ".previous", rollback}} {
		st, err := os.Lstat(protected.path)
		if err != nil {
			return CandidateCleanup{}, err
		}
		current, err := os.OpenFile(protected.path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return CandidateCleanup{}, err
		}
		err = protected.pin.revalidate(st, current)
		_ = current.Close()
		if err != nil {
			return CandidateCleanup{}, err
		}
	}
	latest, err := os.Lstat(bin)
	if err != nil {
		return CandidateCleanup{}, err
	}
	finalParent, err := candidateMetadata("bin", latest)
	if err != nil || finalParent != parent {
		return CandidateCleanup{}, fmt.Errorf("candidate parent changed during snapshot: HOLD")
	}
	sort.Strings(out.Keep)
	sort.Strings(out.Remove)
	sort.Strings(out.Unknown)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	raw, err := json.Marshal(struct {
		Schema                      string
		Installed, Rollback, Parent candidateIdentity
		Items                       []candidateIdentity
	}{"mcpshka.candidate-snapshot/v2", installed.identity, rollback.identity, parent, items})
	if err != nil {
		return CandidateCleanup{}, err
	}
	sum := sha256.Sum256(raw)
	out.Fingerprint = hex.EncodeToString(sum[:])
	if len(out.Unknown) > 0 && len(out.Remove) == 0 {
		out.HoldReason = "verified candidate ownership and writer exclusion unavailable"
	}
	return out, nil
}

// installedBrokerPaths are the root broker and its rollback; candidates with
// their content are never removed.
var installedBrokerPaths = []string{"/usr/local/libexec/mcpshka-broker", "/usr/local/libexec/mcpshka-broker.previous"}

func installedBrokerHashes(ctx context.Context, budget *int64) (map[string]bool, error) {
	out := map[string]bool{}
	for _, path := range installedBrokerPaths {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		pin, err := protectedCandidate(ctx, path, budget)
		if err != nil {
			return nil, err
		}
		_ = pin.file.Close()
		out[pin.identity.SHA256] = true
	}
	return out, nil
}

// releaseProvenance maps content SHA-256 to release version from the receipts
// MCPshka's release worker writes beside its candidates (*.release-plan.json,
// mcpshka-coordinated-release-*.json). Unreadable or malformed receipts prove
// nothing and are skipped.
func releaseProvenance(dir *os.Root, entries []os.DirEntry) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".release-plan.json") && !(strings.HasPrefix(name, "mcpshka-coordinated-release-") && strings.HasSuffix(name, ".json")) {
			continue
		}
		f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > 64<<10 {
			_ = f.Close()
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, 64<<10))
		_ = f.Close()
		if err != nil {
			continue
		}
		var r struct {
			Schema     string `json:"schema"`
			Version    string `json:"version"`
			Candidate  string `json:"candidate_sha256"`
			Controller string `json:"controller_sha256"`
			Broker     string `json:"broker_sha256"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Version == "" || !strings.HasPrefix(r.Schema, "mcpshka.") {
			continue
		}
		for _, sum := range []string{r.Candidate, r.Controller, r.Broker} {
			if len(sum) == 64 {
				// A broker that two releases shared belongs to the newer one.
				if old, ok := out[sum]; !ok || versionBefore(old, r.Version) {
					out[sum] = r.Version
				}
			}
		}
	}
	return out
}

// versionBefore compares dotted numeric versions; anything else is not
// comparable and never counts as older.
func versionBefore(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	if len(pa) != len(pb) {
		return false
	}
	for i := range pa {
		x, errA := strconv.Atoi(pa[i])
		y, errB := strconv.Atoi(pb[i])
		if errA != nil || errB != nil {
			return false
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// removeCandidates unlinks exactly the planned candidates, each only while
// its path still names the snapshotted file.
func (s *Service) removeCandidates(root string, items []candidateIdentity) error {
	dir, err := os.OpenRoot(filepath.Join(root, "bin"))
	if err != nil {
		return err
	}
	defer dir.Close()
	for _, item := range items {
		st, err := dir.Lstat(item.Name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		observed, err := candidateMetadata(item.Name, st)
		expected := item
		expected.SHA256 = ""
		if err != nil || observed != expected {
			return fmt.Errorf("candidate %s changed before removal: HOLD", item.Name)
		}
		if err := dir.Remove(item.Name); err != nil {
			return err
		}
	}
	return nil
}
