// Package dockerlogs reads bounded pages before logs cross the privileged broker.
package dockerlogs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mcpshka/internal/trust"
)

const MaxRecordBytes = 4096
const MaxBytes = 32 << 10 // JSON/base64 framing must also fit the broker's 64 KiB cap.
var containerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type Query struct {
	Container    string `json:"container"`
	SinceSeconds int64  `json:"since_seconds,omitempty"`
	Since        string `json:"since,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	Component    string `json:"component,omitempty"`
	Level        string `json:"level,omitempty"`
	Lines        int    `json:"lines,omitempty"`
	MaxBytes     int    `json:"max_bytes,omitempty"`
}

type Result struct {
	Trust        trust.Label `json:"trust"`
	Name         string      `json:"name"`
	SinceSeconds int64       `json:"since_seconds"`
	Since        string      `json:"since"`
	Until        string      `json:"until"`
	Lines        []string    `json:"lines"`
	Bytes        int         `json:"bytes"`
	Truncated    bool        `json:"truncated"`
	NextCursor   string      `json:"next_cursor,omitempty"`
}

type cursor struct {
	Filter    string    `json:"filter"`
	Container string    `json:"container"`
	Since     string    `json:"since"`
	Until     string    `json:"until"`
	Offset    [2]int64  `json:"offset"`
	Prefix    [2]string `json:"prefix"`
}

type Runner func(context.Context, []string, io.Writer, io.Writer) error

func RunDocker(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/docker", args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

func (q Query) window(now time.Time) (Query, cursor, error) {
	if !containerName.MatchString(q.Container) || len(q.Component) > 256 || strings.ContainsAny(q.Component, "\r\n\x00") || len(q.Cursor) > 2048 {
		return q, cursor{}, errors.New("invalid Docker log query")
	}
	if q.Level != "" && q.Level != "ERROR" && q.Level != "WARNING" && q.Level != "INFO" {
		return q, cursor{}, errors.New("level must be ERROR, WARNING or INFO")
	}
	if q.Lines <= 0 {
		q.Lines = 100
	}
	if q.Lines > 500 {
		q.Lines = 500
	}
	if q.MaxBytes <= 0 {
		q.MaxBytes = MaxBytes
	}
	if q.MaxBytes > MaxBytes {
		q.MaxBytes = MaxBytes
	}
	if q.SinceSeconds <= 0 {
		q.SinceSeconds = 900
	}
	if q.SinceSeconds > 86400 {
		q.SinceSeconds = 86400
	}
	filter := fmt.Sprintf("%x", sha256.Sum256([]byte(q.Component+"\x00"+q.Level)))
	c := cursor{Container: q.Container, Filter: filter, Since: now.Add(-time.Duration(q.SinceSeconds) * time.Second).Format(time.RFC3339Nano), Until: now.Format(time.RFC3339Nano)}
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Container != q.Container || c.Filter != filter || c.Offset[0] < 0 || c.Offset[1] < 0 {
			return q, c, errors.New("invalid or mismatched log cursor")
		}
	} else if q.Since != "" {
		c.Since = q.Since
	}
	start, e1 := time.Parse(time.RFC3339Nano, c.Since)
	end, e2 := time.Parse(time.RFC3339Nano, c.Until)
	if e1 != nil || e2 != nil || start.After(end) || start.Before(now.Add(-24*time.Hour-time.Minute)) || end.After(now.Add(time.Second)) {
		return q, c, errors.New("log window must be within the last 24 hours")
	}
	c.Since = start.UTC().Format(time.RFC3339Nano)
	c.Until = end.UTC().Format(time.RFC3339Nano)
	return q, c, nil
}

type collector struct {
	mu        sync.Mutex
	q         Query
	result    Result
	next      cursor
	full      bool
	wireBytes int
	err       error
}

type stream struct {
	c              *collector
	index          int
	seen           int64
	line           []byte
	clipped        bool
	digest         hash.Hash
	skip           int64
	expected       string
	componentMatch bool
	componentTail  string
	entryLevel     string
	levelToken     string
	oversizedToken bool
}

// Write drains even oversized records, keeping at most one bounded prefix per
// stream. Docker sends application logs on both stdout and stderr.
func (s *stream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		// Filtering and cursor identity see all bytes, including clipped suffixes.
		s.digest.Write(part)
		s.filter(part)
		if i >= 0 {
			s.digest.Write([]byte{'\n'})
		}
		remaining := MaxRecordBytes - len(s.line)
		if len(part) > remaining {
			s.clipped = true
			part = part[:remaining]
		}
		s.line = append(s.line, part...)
		if i < 0 {
			break
		}
		s.flush()
		p = p[i+1:]
	}
	return n, nil
}

func (s *stream) flush() {
	s.seen++
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.seen <= s.skip {
		if s.seen == s.skip && fmt.Sprintf("%x", s.digest.Sum(nil)) != s.expected {
			c.err = errors.New("log cursor expired: logs rotated; start a fresh since window")
		}
		s.reset()
		return
	}
	line := strings.ToValidUTF8(string(s.line), "")
	s.finishLevel()
	matches := (c.q.Component == "" || s.componentMatch) && (c.q.Level == "" || s.entryLevel == c.q.Level)
	if c.full {
		c.result.Truncated = true
		s.reset()
		return
	}
	// Non-matching records still advance the cursor, so empty filtered pages end.
	encoded, _ := json.Marshal(line)
	if matches && (len(c.result.Lines) >= c.q.Lines || c.result.Bytes+len(line) > c.q.MaxBytes || c.wireBytes+len(encoded) > 40<<10) {
		if len(c.result.Lines) == 0 {
			line = line[:min(len(line), c.q.MaxBytes)]
			for !utf8.ValidString(line) {
				line = line[:len(line)-1]
			}
			c.result.Lines = append(c.result.Lines, line)
			c.result.Bytes += len(line)
			c.next.Offset[s.index] = s.seen
			c.next.Prefix[s.index] = fmt.Sprintf("%x", s.digest.Sum(nil))
		}
		c.full = true
		c.result.Truncated = true
		s.reset()
		return
	}
	c.next.Offset[s.index] = s.seen
	c.next.Prefix[s.index] = fmt.Sprintf("%x", s.digest.Sum(nil))
	if matches {
		c.wireBytes += len(encoded)
		c.result.Lines = append(c.result.Lines, line)
		c.result.Bytes += len(line)
		c.result.Truncated = c.result.Truncated || s.clipped
	}
	s.reset()
}
func (s *stream) reset() {
	s.line = s.line[:0]
	s.clipped = false
	s.componentMatch = false
	s.componentTail = ""
	s.entryLevel = ""
	s.levelToken = ""
	s.oversizedToken = false
}
func (s *stream) filter(part []byte) {
	component := s.c.q.Component
	if component != "" && !s.componentMatch {
		text := s.componentTail + string(part)
		s.componentMatch = strings.Contains(text, component)
		s.componentTail = text[max(0, len(text)-len(component)+1):]
	}
	if s.entryLevel != "" {
		return
	}
	for _, c := range part {
		if c >= 'A' && c <= 'Z' {
			if len(s.levelToken) < 8 {
				s.levelToken += string(c)
			} else {
				s.oversizedToken = true
			}
		} else {
			s.finishLevel()
		}
	}
}
func (s *stream) finishLevel() {
	if !s.oversizedToken && s.entryLevel == "" {
		switch s.levelToken {
		case "ERROR", "WARNING", "INFO":
			s.entryLevel = s.levelToken
		}
	}
	s.levelToken = ""
	s.oversizedToken = false
}

func Read(ctx context.Context, q Query, run Runner) (Result, error) {
	q, c, err := q.window(time.Now().UTC())
	if err != nil {
		return Result{}, err
	}
	col := collector{q: q, next: c, result: Result{Trust: trust.Untrusted, Name: q.Container, SinceSeconds: q.SinceSeconds, Since: c.Since, Until: c.Until, Lines: []string{}}}
	out, stderr := &stream{c: &col, index: 0, digest: sha256.New(), skip: c.Offset[0], expected: c.Prefix[0]}, &stream{c: &col, index: 1, digest: sha256.New(), skip: c.Offset[1], expected: c.Prefix[1]}
	args := []string{"logs", "--timestamps", "--since", c.Since, "--until", c.Until, "--tail", "all", q.Container}
	child, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := run(child, args, out, stderr); err != nil {
		return Result{}, errors.New("docker logs command failed (container unavailable, permission denied or timeout)")
	}
	if len(out.line) > 0 || out.clipped {
		out.flush()
	}
	if len(stderr.line) > 0 || stderr.clipped {
		stderr.flush()
	}
	if col.err != nil {
		return Result{}, col.err
	}
	if out.seen < out.skip || stderr.seen < stderr.skip {
		return Result{}, errors.New("log cursor expired: logs rotated; start a fresh since window")
	}
	if col.full {
		raw, _ := json.Marshal(col.next)
		col.result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return col.result, nil
}
