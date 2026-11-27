package storagefabric

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Pin every directory descriptor: a service-writable inbox cannot redirect a
// privileged reader through symlinks or hardlinks, even across path replacement.
func openUploadSource(p string) (*os.File, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return nil, ErrInvalid
	}
	fd, e := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, err := syscall.Openat(fd, part, flags, 0)
		syscall.Close(fd)
		if err != nil {
			return nil, errors.New("unsafe artifact source path")
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), "artifact")
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	u, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || !ok || u.Nlink != 1 || st.Size() > MaxFileBytes {
		f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}
func stageUpload(source *os.File, destination, digest string, size int64) error {
	decoded, e := hex.DecodeString(digest)
	if e != nil || len(decoded) != 32 || size < 0 || size > MaxFileBytes {
		return ErrInvalid
	}
	st, e := source.Stat()
	if e != nil || st.Size() != size {
		return errors.New("artifact size changed")
	}
	f, e := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	good := false
	defer func() {
		f.Close()
		if !good {
			os.Remove(destination)
		}
	}()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(source, size+1))
	if e != nil || n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return errors.New("artifact SHA-256 changed")
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	good = true
	return nil
}
