// Package boundedbuf is a writer that keeps at most Limit bytes and reports
// whether it dropped any; the writer never fails, so a chatty child process
// cannot block on a full pipe.
package boundedbuf

import "bytes"

type Buffer struct {
	Limit     int
	buf       bytes.Buffer
	truncated bool
}

func (b *Buffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := b.Limit - b.buf.Len(); remaining > 0 {
		_, _ = b.buf.Write(p[:min(len(p), remaining)])
	}
	if b.buf.Len() >= b.Limit && n > 0 && b.buf.Len()+n > b.Limit {
		b.truncated = true
	}
	return n, nil
}

func (b *Buffer) Bytes() []byte   { return append([]byte(nil), b.buf.Bytes()...) }
func (b *Buffer) Truncated() bool { return b.truncated }
