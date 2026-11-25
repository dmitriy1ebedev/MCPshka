package budget

import (
	"bytes"
	"fmt"
	"strings"
)

// Output retains bounded head/tail data without closing a noisy child's pipe.
// Set Limit before the first write; zero preserves the executor's 8 KiB budget.
type Output struct {
	Limit      int
	head, tail []byte
	dropped    int64
	// fails keeps the failure lines (--- FAIL, FAIL pkg, panic:) wherever
	// they were written: 0.19.0's sealed run lost its only FAIL line in the
	// omitted middle and the cause took two more hours (attempts 1–2).
	fails   []string
	failLen int
	partial []byte
}

const failBudget = 768

func failLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte("--- FAIL")) || bytes.HasPrefix(line, []byte("FAIL\t")) || bytes.HasPrefix(line, []byte("FAIL ")) ||
		bytes.HasPrefix(line, []byte("panic: ")) || bytes.HasPrefix(line, []byte("fatal error: "))
}

func (o *Output) scan(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(o.partial)+len(p) <= 512 {
				o.partial = append(o.partial, p...)
			} else {
				o.partial = o.partial[:0]
			}
			return
		}
		line := append(o.partial, p[:i]...)
		o.partial, p = o.partial[:0], p[i+1:]
		if failLine(line) && o.failLen+len(line)+1 <= failBudget {
			o.fails = append(o.fails, string(line))
			o.failLen += len(line) + 1
		}
	}
}

func (o *Output) limit() int {
	if o.Limit <= 0 {
		return 8192
	}
	return o.Limit
}

func (o *Output) Write(p []byte) (int, error) {
	n := len(p)
	o.scan(p)
	dataLimit := max(0, o.limit()-1024)
	headLimit := min(2048, dataLimit)
	tailLimit := dataLimit - headLimit
	if room := headLimit - len(o.head); room > 0 {
		take := min(room, len(p))
		o.head = append(o.head, p[:take]...)
		p = p[take:]
	}
	if len(p) >= tailLimit {
		o.dropped += int64(len(o.tail) + len(p) - tailLimit)
		o.tail = append(o.tail[:0], p[len(p)-tailLimit:]...)
	} else {
		if over := len(o.tail) + len(p) - tailLimit; over > 0 {
			copy(o.tail, o.tail[over:])
			o.tail = o.tail[:len(o.tail)-over]
			o.dropped += int64(over)
		}
		o.tail = append(o.tail, p...)
	}
	return n, nil
}

func (o *Output) String() string {
	text := string(o.head) + string(o.tail)
	if o.dropped > 0 {
		var lost []string
		for _, f := range o.fails {
			if !bytes.Contains(o.head, []byte(f)) && !bytes.Contains(o.tail, []byte(f)) {
				lost = append(lost, f)
			}
		}
		marker := fmt.Sprintf("[mcpshka] … %d bytes omitted …", o.dropped)
		if len(lost) > 0 {
			marker += "\n[mcpshka] failures inside the omitted part:\n" + strings.Join(lost, "\n")
		}
		text = fmt.Sprintf("%s\n%s\n%s", o.head, marker, o.tail)
	}
	text = strings.ToValidUTF8(text, "?")
	if len(text) > o.limit() {
		text = strings.ToValidUTF8(text[:o.limit()], "?")
	}
	return text
}
