package projectlease

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Native loose objects avoid running repository-configured Git writes as root.
// New objects/refs keep the object store owner's UID/GID; existing files stay intact.
func recoveryObjectOwner(common string) (int, int, error) {
	info, err := os.Stat(filepath.Join(common, "objects"))
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("Git owner unavailable")
	}
	return int(st.Uid), int(st.Gid), nil
}

func recoveryDirectory(path string, uid, gid int) error {
	if err := os.Mkdir(path, 0755); err == nil {
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
		return syncDir(filepath.Dir(path))
	} else if !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Git directory alias: HOLD")
	}
	return nil
}

func recoveryObject(common, kind string, body io.Reader, size int64) (string, error) {
	uid, gid, err := recoveryObjectOwner(common)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Join(common, "objects"), "tmp_mcpshka_")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chown(uid, gid); err != nil {
		return "", err
	}
	hash := sha1.New()
	compressed := zlib.NewWriter(f)
	writer := io.MultiWriter(hash, compressed)
	if _, err := fmt.Fprintf(writer, "%s %d%c", kind, size, 0); err != nil {
		return "", err
	}
	if _, err := io.CopyN(writer, body, size); err != nil {
		return "", err
	}
	if err := compressed.Close(); err != nil {
		return "", err
	}
	if err := f.Chmod(0444); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	shard := filepath.Join(common, "objects", sum[:2])
	if err := recoveryDirectory(shard, uid, gid); err != nil {
		return "", err
	}
	target := filepath.Join(shard, sum[2:])
	if err := os.Link(f.Name(), target); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("Git object alias: HOLD")
		}
		existing, err := os.Open(target)
		if err != nil {
			return "", err
		}
		defer existing.Close()
		raw, err := zlib.NewReader(existing)
		if err != nil {
			return "", err
		}
		defer raw.Close()
		check := sha1.New()
		n, err := io.Copy(check, io.LimitReader(raw, recoverySnapshotBudget+(8<<20)))
		if err != nil || n >= recoverySnapshotBudget+(8<<20) || hex.EncodeToString(check.Sum(nil)) != sum {
			return "", errors.New("existing recovery object is corrupt: HOLD")
		}
	}
	if err := syncDir(shard); err != nil {
		return "", err
	}
	return sum, nil
}

func storeRecoverySnapshot(common, id string, archive *os.File) (string, error) {
	if err := recoveryGitWriteScope(common, id); err != nil {
		return "", err
	}
	info, err := archive.Stat()
	if err != nil {
		return "", err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	blob, err := recoveryObject(common, "blob", archive, info.Size())
	if err != nil {
		return "", err
	}
	rawBlob, _ := hex.DecodeString(blob)
	treeBody := append([]byte("100644 worktree.tar\x00"), rawBlob...)
	tree, err := recoveryObject(common, "tree", bytes.NewReader(treeBody), int64(len(treeBody)))
	if err != nil {
		return "", err
	}
	commitBody := "tree " + tree + "\nauthor MCPshka recovery <recovery@localhost> 0 +0000\ncommitter MCPshka recovery <recovery@localhost> 0 +0000\n\nPreserved raw worktree and original index before lease recovery\n"
	commit, err := recoveryObject(common, "commit", strings.NewReader(commitBody), int64(len(commitBody)))
	if err != nil {
		return "", err
	}
	// A loose recovery ref must not shadow an unrelated packed ref.
	packed, err := os.Open(filepath.Join(common, "packed-refs"))
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(packed, (2<<20)+1))
		packed.Close()
		if readErr != nil || len(data) > 2<<20 {
			return "", errors.New("packed reference evidence unavailable: HOLD")
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[1] == "refs/heads/mcpshka-recovery/"+id && fields[0] != commit {
				return "", errors.New("packed recovery reference differs: HOLD")
			}
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	uid, gid, err := recoveryObjectOwner(common)
	if err != nil {
		return "", err
	}
	for _, rel := range []string{"refs", "refs/heads", "refs/heads/mcpshka-recovery"} {
		if err := recoveryDirectory(filepath.Join(common, rel), uid, gid); err != nil {
			return "", err
		}
	}
	parent := filepath.Join(common, "refs", "heads", "mcpshka-recovery")
	tmp, err := os.CreateTemp(parent, ".snapshot-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chown(uid, gid); err != nil {
		return "", err
	}
	if _, err := io.WriteString(tmp, commit+"\n"); err != nil {
		return "", err
	}
	if err := tmp.Chmod(0644); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	target := filepath.Join(parent, id)
	if err := os.Link(tmp.Name(), target); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("recovery reference alias: HOLD")
		}
		old, err := os.ReadFile(target)
		if err != nil || string(old) != commit+"\n" {
			return "", errors.New("recovery reference already differs: HOLD")
		}
	}
	if err := syncDir(parent); err != nil {
		return "", err
	}
	return "refs/heads/mcpshka-recovery/" + id, nil
}
