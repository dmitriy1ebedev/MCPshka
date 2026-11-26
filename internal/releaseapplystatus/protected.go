package releaseapplystatus

import (
	"errors"
	"io"
	"os"
	"syscall"
)

func ReadProtected(path string) ([]byte, os.FileInfo, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > 65536 || st.Size() == 0 {
		return nil, nil, errors.New("invalid protected configuration file")
	}
	data, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil {
		return nil, nil, e
	}
	if len(data) > 65536 {
		return nil, nil, errors.New("configuration file limit")
	}
	return data, st, nil
}
