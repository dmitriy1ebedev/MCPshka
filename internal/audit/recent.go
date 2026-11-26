package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Recent scans the active audit journal and at most three rotated segments.
// The coverage flag only describes readable segments, not which events existed.
func (l *Logger) Recent(ctx context.Context, since time.Time, visit func(Event)) (bool, error) {
	dir, err := os.Open(filepath.Dir(l.path))
	if err != nil {
		return false, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(8192)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	prefix := filepath.Base(l.path) + "."
	segments := []string{}
	for _, name := range names {
		suffix, ok := strings.CutPrefix(name, prefix)
		n, e := strconv.ParseInt(suffix, 10, 64)
		if ok && e == nil && !time.Unix(0, n).Before(since) {
			segments = append(segments, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(segments)))
	complete := len(segments) <= 3 && len(names) < 8192
	for _, name := range append([]string{filepath.Base(l.path)}, segments[:min(3, len(segments))]...) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		ok, err := scanRecent(ctx, filepath.Join(filepath.Dir(l.path), name), since, visit)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		complete = complete && ok
	}
	return complete, nil
}

func scanRecent(ctx context.Context, path string, since time.Time, visit func(Event)) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	const budget = 4 << 20
	limit := min(info.Size(), budget)
	sc := bufio.NewScanner(io.LimitReader(f, limit))
	sc.Buffer(make([]byte, 8192), 256<<10)
	complete := info.Size() <= budget
	for sc.Scan() {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			complete = false
			continue
		}
		if !e.Time.Before(since) {
			visit(e)
		}
	}
	return complete && sc.Err() == nil, sc.Err()
}
