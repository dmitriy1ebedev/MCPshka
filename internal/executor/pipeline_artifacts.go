package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/projectprofile"
)

const MaxPipelineArtifact = int64(64 << 20)
const MaxPipelineArtifacts = int64(256 << 20)

type PipelineArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// The worker seals only declared regular files, before disposable scratch cleanup.
func sealPipelineArtifacts(paths []string, scratch, dest string, owner uint32) (retErr error) {
	if len(paths) == 0 || projectprofile.ValidateArtifacts(paths) != nil {
		return ErrHold
	}
	root, err := os.OpenRoot(filepath.Join(scratch, "project"))
	if err != nil {
		return err
	}
	defer root.Close()
	if filepath.Dir(dest) == filepath.Join(AuthorityRoot, "pipeline-artifacts") {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = PrunePipelineArtifacts(ctx)
		cancel()
		if err != nil {
			return err
		}
	}
	if err = checkPipelineArchiveBudget(filepath.Dir(dest)); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	if err = os.Mkdir(dest, 0700); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(dest)
		}
	}()
	var artifacts []PipelineArtifact
	var total int64
	for i, p := range paths {
		// Reject symlinks at every component, including aliases inside the sandbox.
		at := ""
		for _, part := range strings.Split(p, "/") {
			at = filepath.Join(at, part)
			info, e := root.Lstat(at)
			if e != nil {
				return e
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return ErrHold
			}
		}
		raw, e := readPipelineSource(root, p, owner)
		if e != nil {
			return e
		}
		total += int64(len(raw))
		if total > MaxPipelineArtifacts {
			return ErrHold
		}
		if e = localWrite(filepath.Join(dest, strconv.Itoa(i)), raw); e != nil {
			return e
		}
		hash := sha256.Sum256(raw)
		artifacts = append(artifacts, PipelineArtifact{Path: p, SHA256: hex.EncodeToString(hash[:]), Bytes: int64(len(raw))})
	}
	raw, err := json.Marshal(artifacts)
	if err != nil {
		return err
	}
	return localWrite(filepath.Join(dest, "receipt.json"), raw)
}

func PipelineReceipt(ctx context.Context, permit string) ([]PipelineArtifact, error) {
	m, err := successfulLocalPermit(ctx, permit)
	if err != nil {
		return nil, err
	}
	if m.Purpose != "project-recipe-test-v1" {
		return nil, ErrHold
	}
	var spec RunSpec
	for _, s := range m.Runs {
		spec = s
	}
	if spec.Stage != "package" || len(spec.Artifacts) == 0 {
		return nil, ErrHold
	}
	raw, err := localRead(filepath.Join(AuthorityRoot, "pipeline-artifacts", permit, "receipt.json"), 16384, 0)
	if err != nil {
		return nil, err
	}
	var artifacts []PipelineArtifact
	if json.Unmarshal(raw, &artifacts) != nil || len(artifacts) != len(spec.Artifacts) {
		return nil, ErrHold
	}
	var total int64
	for i, a := range artifacts {
		total += a.Bytes
		if a.Path != spec.Artifacts[i] || !digestPattern.MatchString(a.SHA256) || a.Bytes < 0 || a.Bytes > MaxPipelineArtifact || total > MaxPipelineArtifacts {
			return nil, ErrHold
		}
	}
	return artifacts, nil
}

func PipelineRead(ctx context.Context, permit string, index int, offset int64) ([]byte, bool, error) {
	artifacts, err := PipelineReceipt(ctx, permit)
	if err != nil {
		return nil, false, err
	}
	if index < 0 || index >= len(artifacts) || offset < 0 || offset > artifacts[index].Bytes {
		return nil, false, ErrHold
	}
	// Read the sealed root-owned image using the same bounded ownership checks as releases.
	path := filepath.Join(AuthorityRoot, "pipeline-artifacts", permit, strconv.Itoa(index))
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var st syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &st) != nil || st.Uid != 0 || st.Nlink != 1 {
		return nil, false, ErrHold
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || info.Size() != artifacts[index].Bytes {
		return nil, false, ErrHold
	}
	raw := make([]byte, min(int64(32768), info.Size()-offset))
	n, err := f.ReadAt(raw, offset)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	return raw[:n], offset+int64(n) == info.Size(), nil
}

// PrunePipelineArtifacts removes only sealed, fenced receipts older than seven days.
func PrunePipelineArtifacts(ctx context.Context) error {
	base := filepath.Join(AuthorityRoot, "pipeline-artifacts")
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 1000 {
		return fmt.Errorf("pipeline retention scan exceeds 1000 entries")
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		info, e := os.Stat(filepath.Join(dir, "receipt.json"))
		if e != nil || time.Since(info.ModTime()) < 7*24*time.Hour {
			continue
		}
		c, m, e := OpenPermit(entry.Name())
		if e != nil {
			continue
		}
		state, e := c.Inspect(ctx, m.Pin)
		if e != nil || state.State != "FENCED" {
			continue
		}
		if e = os.RemoveAll(dir); e != nil {
			return e
		}
	}
	return nil
}

func checkPipelineArchiveBudget(base string) error {
	var total int64
	count := 0
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == base {
			return nil
		}
		if err != nil {
			return err
		}
		count++
		if count > 20000 {
			return ErrHold
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrHold
		}
		if !d.IsDir() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Reserve one maximum-sized package before copying; never evict active evidence for space.
	if total+MaxPipelineArtifacts > 512<<20 {
		return fmt.Errorf("pipeline sealed archive capacity reached")
	}
	return nil
}

// os.Root confines the open even if hostile descendants replace a parent directory.
func readPipelineSource(root *os.Root, path string, owner uint32) ([]byte, error) {
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var before, after syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Uid != owner || before.Nlink != 1 || before.Size > MaxPipelineArtifact {
		return nil, ErrHold
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxPipelineArtifact+1))
	if err != nil || int64(len(raw)) != before.Size || syscall.Fstat(int(f.Fd()), &after) != nil || before.Mtim != after.Mtim || before.Ctim != after.Ctim || before.Size != after.Size {
		return nil, ErrHold
	}
	return raw, nil
}
