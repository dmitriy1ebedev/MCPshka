package filesystem

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

// maxLineScan bounds the scan for a line start (fs_read line=N).
const maxLineScan = 64 << 20

// LineOffset is the byte offset where 1-based line starts, so fs_read can
// open a file at a line number from fs_find (RDC read_file offset in lines).
func (s *Service) LineOffset(path string, line int) (int64, error) {
	if line < 1 {
		return 0, errors.New("line must be >= 1")
	}
	real, err := s.policy.ResolveExisting(path)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(real)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.LimitReader(f, maxLineScan), 64<<10)
	var off int64
	for seen := 1; seen < line; seen++ {
		chunk, err := r.ReadSlice('\n')
		for errors.Is(err, bufio.ErrBufferFull) {
			off += int64(len(chunk))
			chunk, err = r.ReadSlice('\n')
		}
		off += int64(len(chunk))
		if err != nil {
			return 0, fmt.Errorf("line %d is past the end of the file (%d lines)", line, seen)
		}
	}
	return off, nil
}
