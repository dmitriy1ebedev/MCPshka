package agents

import (
	"io"
	"os"
	"syscall"
)

// A report is process output, not permission to follow a substituted file.
func readReportTail(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	size := min(info.Size(), int64(64<<10))
	data := make([]byte, int(size))
	n, err := f.ReadAt(data, info.Size()-size)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return data[:n], nil
}

func writeReport(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0640)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || st.Nlink != 1 {
		return os.ErrInvalid
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}
