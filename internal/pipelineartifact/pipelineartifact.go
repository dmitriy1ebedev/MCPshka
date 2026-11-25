// Package pipelineartifact seals and serves the files a recipe package stage
// exports (ROADMAP S5.LEAN: split out of internal/executor). The executor
// keeps the permit checks; this package owns the file mechanics: regular,
// unaliased files only, bounded sizes, root-owned 0400 images.
package pipelineartifact

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
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/projectprofile"
)

const MaxArtifact = int64(64 << 20)
const MaxTotal = int64(256 << 20)

// ErrRejected marks an unsafe or out-of-contract artifact.
var ErrRejected = errors.New("pipeline artifact rejected")

var (
	idPattern     = regexp.MustCompile("^[0-9a-f]{32}$")
	digestPattern = regexp.MustCompile("^[0-9a-f]{64}$")
)

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Seal copies only declared regular files, before disposable scratch cleanup.
func Seal(paths []string, scratch, dest string, owner uint32) (retErr error) {
	if len(paths) == 0 || projectprofile.ValidateArtifacts(paths) != nil {
		return ErrRejected
	}
	root, err := os.OpenRoot(filepath.Join(scratch, "project"))
	if err != nil {
		return err
	}
	defer root.Close()
	if err = checkArchiveBudget(filepath.Dir(dest)); err != nil {
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
	var artifacts []Artifact
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
				return ErrRejected
			}
		}
		raw, e := readSource(root, p, owner)
		if e != nil {
			return e
		}
		total += int64(len(raw))
		if total > MaxTotal {
			return ErrRejected
		}
		if e = writeSealed(filepath.Join(dest, strconv.Itoa(i)), raw); e != nil {
			return e
		}
		hash := sha256.Sum256(raw)
		artifacts = append(artifacts, Artifact{Path: p, SHA256: hex.EncodeToString(hash[:]), Bytes: int64(len(raw))})
	}
	raw, err := json.Marshal(artifacts)
	if err != nil {
		return err
	}
	return writeSealed(filepath.Join(dest, "receipt.json"), raw)
}

// CheckReceipt binds a sealed receipt to the run's declared artifact paths.
func CheckReceipt(raw []byte, declared []string) ([]Artifact, error) {
	var artifacts []Artifact
	if json.Unmarshal(raw, &artifacts) != nil || len(artifacts) != len(declared) {
		return nil, ErrRejected
	}
	var total int64
	for i, a := range artifacts {
		total += a.Bytes
		if a.Path != declared[i] || !digestPattern.MatchString(a.SHA256) || a.Bytes < 0 || a.Bytes > MaxArtifact || total > MaxTotal {
			return nil, ErrRejected
		}
	}
	return artifacts, nil
}

// ReadAt reads up to 32 KiB of a sealed root-owned image of the given size,
// with the same bounded ownership checks as releases.
func ReadAt(path string, size, offset int64) ([]byte, bool, error) {
	if offset < 0 || offset > size {
		return nil, false, ErrRejected
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var st syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &st) != nil || st.Uid != 0 || st.Nlink != 1 {
		return nil, false, ErrRejected
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || info.Size() != size {
		return nil, false, ErrRejected
	}
	raw := make([]byte, min(int64(32768), info.Size()-offset))
	n, err := f.ReadAt(raw, offset)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	return raw[:n], offset+int64(n) == info.Size(), nil
}

// Prune removes sealed receipts older than seven days whose permit fenced
// reports as fenced.
func Prune(ctx context.Context, base string, fenced func(ctx context.Context, permit string) bool) error {
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
		if e != nil || time.Since(info.ModTime()) < 7*24*time.Hour || !fenced(ctx, entry.Name()) {
			continue
		}
		if e = os.RemoveAll(dir); e != nil {
			return e
		}
	}
	return nil
}

func checkArchiveBudget(base string) error {
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
		if count > 20000 || d.Type()&os.ModeSymlink != 0 {
			return ErrRejected
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
	if total+MaxTotal > 512<<20 {
		return fmt.Errorf("pipeline sealed archive capacity reached")
	}
	return nil
}

// os.Root confines the open even if hostile descendants replace a parent directory.
func readSource(root *os.Root, path string, owner uint32) ([]byte, error) {
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var before, after syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Uid != owner || before.Nlink != 1 || before.Size > MaxArtifact {
		return nil, ErrRejected
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxArtifact+1))
	if err != nil || int64(len(raw)) != before.Size || syscall.Fstat(int(f.Fd()), &after) != nil || before.Mtim != after.Mtim || before.Ctim != after.Ctim || before.Size != after.Size {
		return nil, ErrRejected
	}
	return raw, nil
}

func writeSealed(path string, raw []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if e != nil {
		return e
	}
	_, e = f.Write(raw)
	if e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	return e
}
