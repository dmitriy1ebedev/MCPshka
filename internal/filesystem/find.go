package filesystem

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/trust"
)

const (
	DefaultFindTimeout = 5 * time.Second
	MaxFindTimeout     = 20 * time.Second
	maxFindIgnore      = 16
	deniedExamples     = 3
)

type Match struct {
	Path string "json:\"path\""
	Line int    "json:\"line,omitempty\""
	Text string "json:\"text,omitempty\""
	// Before and After are up to ContextLines neighbouring lines (Desktop
	// Commander contextLines), clipped like Text.
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

// FindQuery is one bounded search. There is no server-side session: Cursor
// from the previous answer continues the walk, and not sending it stops it.
type FindQuery struct {
	Path       string
	Pattern    string
	Glob       string
	Mode       string // content (default) or name
	Regex      bool   // Pattern is an RE2 expression instead of literal text
	Ignore     []string
	Cursor     string
	MaxResults int
	Timeout    time.Duration
	// ContextLines adds 0..5 lines around each content match.
	ContextLines int
}

// FindSkipped counts what the walk could not or would not search.
type FindSkipped struct {
	Denied int `json:"denied"` // unreadable folders and files
	Large  int `json:"large"`  // over MaxSearchFileSize
	Binary int `json:"binary"` // NUL byte in the first block
}

type FindResult struct {
	Trust   trust.Label `json:"trust"`
	Root    string      `json:"root"`
	Mode    string      `json:"mode"`
	Pattern string      `json:"pattern"`
	Engine  string      `json:"engine"`
	Matches []Match     `json:"matches"`
	// Status: completed, partial (finished, some paths skipped as denied),
	// max_results or timed_out (continue with cursor), failed (nothing searched).
	Status         string      `json:"status"`
	Reason         string      `json:"reason,omitempty"`
	Cursor         string      `json:"cursor,omitempty"`
	Skipped        FindSkipped `json:"skipped"`
	DeniedExamples []string    `json:"denied_examples,omitempty"`
	Truncated      bool        `json:"truncated"`
}

// Find keeps the short call for a literal search with default bounds.
func (s *Service) Find(path, pattern, mode string, maxResults int) (FindResult, error) {
	return s.Search(FindQuery{Path: path, Pattern: pattern, Mode: mode, MaxResults: maxResults})
}

// Search walks the allowed tree in a stable order within a time budget. One
// unreadable folder never cancels matches found elsewhere: it is counted in
// Skipped and shown in DeniedExamples. Permissions and roots are not relaxed.
func (s *Service) Search(q FindQuery) (FindResult, error) {
	if q.Pattern == "" {
		return FindResult{}, errors.New("pattern is empty")
	}
	if q.Glob != "" {
		if _, err := filepath.Match(q.Glob, ""); err != nil {
			return FindResult{}, err
		}
	}
	root, err := s.policy.ResolveExisting(q.Path)
	if err != nil {
		return FindResult{}, err
	}
	if q.Mode == "" {
		q.Mode = "content"
	}
	if q.Mode != "content" && q.Mode != "name" {
		return FindResult{}, errors.New("mode must be content or name")
	}
	if q.MaxResults <= 0 {
		q.MaxResults = DefaultFindLimit
	}
	q.MaxResults = min(q.MaxResults, MaxFindLimit)
	if q.Timeout <= 0 {
		q.Timeout = DefaultFindTimeout
	}
	q.Timeout = min(q.Timeout, MaxFindTimeout)
	if len(q.Ignore) > maxFindIgnore {
		return FindResult{}, errors.New("at most 16 ignore patterns")
	}
	for _, g := range q.Ignore {
		if _, err := filepath.Match(g, ""); err != nil {
			return FindResult{}, errors.New("bad ignore pattern " + strconv.Quote(g))
		}
	}
	hit := func(b []byte) bool { return bytes.Contains(b, []byte(q.Pattern)) }
	if q.Regex {
		re, err := regexp.Compile(q.Pattern)
		if err != nil {
			return FindResult{}, err
		}
		hit = re.Match
	}
	afterKey, afterLine, err := parseFindCursor(q.Cursor)
	if err != nil {
		return FindResult{}, err
	}

	out := FindResult{Trust: trust.Untrusted, Root: root, Mode: q.Mode, Pattern: q.Pattern, Engine: "pure-go", Matches: []Match{}, Status: "completed"}
	deadline := time.Now().Add(q.Timeout)
	done, searched := "", 0 // done: last fully handled entry, the resume point
	stop := func(status, rel string, line int) error {
		out.Status, out.Truncated, out.Cursor = status, true, strconv.Itoa(line)+":"+rel
		return errResultLimit
	}
	denied := func(path string) {
		out.Skipped.Denied++
		if len(out.DeniedExamples) < deniedExamples {
			out.DeniedExamples = append(out.DeniedExamples, path)
		}
	}
	walkErr := filepath.WalkDir(root, func(path string, de fs.DirEntry, werr error) error {
		if path == root && werr == nil && de.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if path == root {
			rel = filepath.Base(path) // root is a single file
		}
		key := walkKey(rel)
		if werr != nil {
			denied(path)
			if de != nil && de.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if de.IsDir() {
			if shouldSkipDir(de.Name()) || ignored(q.Ignore, rel, de.Name()) {
				return filepath.SkipDir
			}
			if key < afterKey && !strings.HasPrefix(afterKey, key+"\x00") {
				return filepath.SkipDir // wholly before the cursor
			}
		}
		if de.Type()&os.ModeSymlink != 0 || ignored(q.Ignore, rel, de.Name()) {
			return nil
		}
		from := 0
		if key < afterKey || (key == afterKey && afterLine == 0) {
			return nil
		} else if key == afterKey {
			from = afterLine
		}
		if time.Now().After(deadline) {
			return stop("timed_out", done, 0)
		}
		if q.Mode == "name" {
			searched++
			if hit([]byte(de.Name())) {
				out.Matches = append(out.Matches, Match{Path: path})
				if len(out.Matches) >= q.MaxResults {
					return stop("max_results", rel, 0)
				}
			}
			done = rel
			return nil
		}
		if de.IsDir() {
			return nil
		}
		info, err := de.Info()
		if err != nil {
			denied(path)
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if q.Glob != "" && !ignored([]string{q.Glob}, rel, de.Name()) {
			done = rel
			return nil
		}
		if info.Size() > MaxSearchFileSize {
			out.Skipped.Large++
			return nil
		}
		line, err := searchFile(path, hit, from, q.MaxResults, min(max(q.ContextLines, 0), 5), &out)
		switch {
		case errors.Is(err, errResultLimit):
			return stop("max_results", rel, line)
		case errors.Is(err, errBinary):
			out.Skipped.Binary++
		case err != nil:
			denied(path)
		default:
			searched++
		}
		done = rel
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errResultLimit) {
		return FindResult{}, walkErr
	}
	switch {
	case out.Cursor != "":
		out.Reason = map[string]string{"max_results": "result limit reached; pass cursor to continue", "timed_out": "time budget spent; pass cursor to continue"}[out.Status]
	case out.Skipped.Denied > 0 && searched == 0 && len(out.Matches) == 0:
		out.Status, out.Reason = "failed", "nothing could be read under this path"
	case out.Skipped.Denied > 0:
		out.Status, out.Reason = "partial", "some paths are not readable by MCPshka; see denied_examples"
	}
	return out, nil
}

var errBinary = errors.New("binary file")

// searchFile scans lines after line `from`; on the result limit it returns
// the line to resume after. With ctx > 0 each match carries up to ctx lines
// before and after it, taken from the same single read of the file.
func searchFile(path string, hit func([]byte) bool, from, maxResults, ctx int, out *FindResult) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	if head, _ := r.Peek(512); bytes.IndexByte(head, 0) >= 0 {
		return 0, errBinary
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, MaxSearchFileSize+1024)
	var prev []string // the last ctx lines
	var open []int    // matches still collecting After
	clip := func(s string) string {
		if len(s) > 300 {
			return s[:300]
		}
		return s
	}
	follow := func(text string) {
		keep := open[:0]
		for _, i := range open {
			m := &out.Matches[i]
			if m.After = append(m.After, text); len(m.After) < ctx {
				keep = append(keep, i)
			}
		}
		open = keep
	}
	for line := 1; sc.Scan(); line++ {
		text := ""
		if ctx > 0 {
			text = clip(sc.Text())
			follow(text)
		}
		if line > from && hit(sc.Bytes()) {
			if ctx == 0 {
				text = clip(sc.Text())
			}
			m := Match{Path: path, Line: line, Text: text}
			if len(prev) > 0 {
				m.Before = append([]string(nil), prev...)
			}
			out.Matches = append(out.Matches, m)
			if ctx > 0 {
				open = append(open, len(out.Matches)-1)
			}
			if len(out.Matches) >= maxResults {
				// The last match still gets its following lines.
				for k := 0; k < ctx && len(open) > 0 && sc.Scan(); k++ {
					follow(clip(sc.Text()))
				}
				return line, errResultLimit
			}
		}
		if ctx > 0 {
			if prev = append(prev, text); len(prev) > ctx {
				prev = prev[1:]
			}
		}
	}
	return 0, sc.Err()
}

// walkKey orders relative paths exactly as WalkDir visits them: names sorted
// inside each folder, a folder before its children.
func walkKey(rel string) string {
	return strings.ReplaceAll(rel, string(filepath.Separator), "\x00")
}

func parseFindCursor(c string) (string, int, error) {
	if c == "" {
		return "", 0, nil
	}
	n, rel, ok := strings.Cut(c, ":")
	line, err := strconv.Atoi(n)
	if !ok || err != nil || line < 0 {
		return "", 0, errors.New("cursor is not one returned by fs_find")
	}
	return walkKey(rel), line, nil
}

func ignored(globs []string, rel, name string) bool {
	for _, g := range globs {
		if ok, _ := filepath.Match(g, name); ok {
			return true
		}
		if ok, _ := filepath.Match(g, rel); ok {
			return true
		}
	}
	return false
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor":
		return true
	default:
		return false
	}
}
