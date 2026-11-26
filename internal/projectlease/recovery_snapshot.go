package projectlease

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const recoverySnapshotBudget = 64 << 20

// recoveryGitOutputBudget bounds every Git read of lease recovery and
// retirement. It scales with the repository (ls-files prints a line per
// file), so it is one budget for all of them: an 8 KiB proof cap failed at
// ~1170 tracked files (BUG-20261103-25, 1227 files in MCPshka).
const recoveryGitOutputBudget = 2 << 20

type recoveryOutput struct{ bytes.Buffer }

func (b *recoveryOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > recoveryGitOutputBudget {
		return 0, errors.New("recovery Git output budget exceeded")
	}
	return b.Buffer.Write(p)
}

func recoveryGitInput(ctx context.Context, root string, input io.Reader, args ...string) (string, error) {
	cmd := retirementGitCommand(ctx, root, args...)
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=MCPshka recovery", "GIT_AUTHOR_EMAIL=recovery@localhost", "GIT_COMMITTER_NAME=MCPshka recovery", "GIT_COMMITTER_EMAIL=recovery@localhost")
	cmd.Stdin = input
	var out recoveryOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("bounded recovery Git operation failed: %w", err)
	}
	if len(args) > 0 && args[0] == "ls-files" {
		return out.String(), nil
	}
	return strings.TrimSpace(out.String()), nil
}

// Archive raw bytes, symlinks, permissions and the original index. Unlike
// git add/stash this runs no filters, changes no index, and resets no files.
func recoveryArchive(ctx context.Context, root string, dst io.Writer) error {
	if err := rejectRecoveryGitExecutables(ctx, root); err != nil {
		return err
	}
	names, err := recoveryGitInput(ctx, root, nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	paths := strings.Split(strings.TrimSuffix(names, "\x00"), "\x00")
	sort.Strings(paths)
	if len(paths) > 10000 {
		return errors.New("recovery file count budget exceeded")
	}
	tw := tar.NewWriter(dst)
	defer tw.Close()
	used := int64(0)
	add := func(name string, body io.Reader, size int64, mode int64, kind byte, link string) error {
		used += size
		if size < 0 || used > recoverySnapshotBudget {
			return errors.New("recovery snapshot exceeds 64 MiB: HOLD")
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, Typeflag: kind, Linkname: link}); err != nil {
			return err
		}
		if size > 0 {
			if n, err := io.CopyN(tw, body, size); err != nil || n != size {
				return errors.New("recovery source changed while archiving")
			}
		}
		return nil
	}
	head, err := retirementGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if err := add("recovery/HEAD", strings.NewReader(head), int64(len(head)), 0600, tar.TypeReg, ""); err != nil {
		return err
	}
	index, err := retirementGit(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return err
	}
	if _, err := os.Stat(index); err == nil {
		data, err := os.Open(index)
		if err != nil {
			return err
		}
		info, err := data.Stat()
		if err != nil {
			data.Close()
			return err
		}
		err = add("recovery/index", data, info.Size(), 0600, tar.TypeReg, "")
		data.Close()
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	previous := ""
	for _, name := range paths {
		if name == "" || name == previous {
			continue
		}
		previous = name
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return errors.New("unsafe recovery path")
		}
		full := filepath.Join(root, name)
		parent, err := filepath.EvalSymlinks(filepath.Dir(full))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if parent != root && !strings.HasPrefix(parent, root+"/") {
			return errors.New("recovery path escapes worktree")
		}
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			if err := add("worktree/"+name, nil, 0, int64(info.Mode().Perm()), tar.TypeSymlink, target); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return errors.New("nonregular recovery file: HOLD")
		}
		file, err := os.Open(full)
		if err != nil {
			return err
		}
		err = add("worktree/"+name, file, info.Size(), int64(info.Mode().Perm()), tar.TypeReg, "")
		file.Close()
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

func recoveryDigest(ctx context.Context, root string) (string, error) {
	hash := sha256.New()
	if err := recoveryArchive(ctx, root, hash); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Service) preserveRecoveryWork(ctx context.Context, p RootSealedRecovery) (string, error) {
	if !retirementHex(p.WorktreeSHA256, 64) {
		return "", errors.New("raw work snapshot pin required")
	}
	if err := rootSealedDirectory(p.Path); err != nil {
		return "", err
	}
	if err := rootSealedDirectory(p.GitCommonDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(s.dir, "recovery-snapshot-")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	if err := recoveryArchive(ctx, p.Path, io.MultiWriter(file, hash)); err != nil {
		return "", err
	}
	if hex.EncodeToString(hash.Sum(nil)) != p.WorktreeSHA256 {
		return "", errors.New("worktree changed after recovery preview: HOLD")
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	ref, err := storeRecoverySnapshot(p.GitCommonDir, p.JournalID, file)
	if err != nil {
		return "", err
	}
	verify := retirementGitCommand(ctx, p.Path, "show", ref+":worktree.tar")
	restored := sha256.New()
	verify.Stdout = restored
	if err := verify.Run(); err != nil || hex.EncodeToString(restored.Sum(nil)) != p.WorktreeSHA256 {
		return "", errors.New("durable recovery snapshot verification failed: HOLD")
	}
	return ref, nil
}
