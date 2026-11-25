package gitobjects

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"mcpshka/internal/failure"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

const MaxBytes = int64(256 << 20)
const MaxCount = 32768

type Link struct{ OID, Kind string }
type ContextReader struct {
	Ctx context.Context
	R   io.Reader
}

func (r ContextReader) Read(p []byte) (int, error) {
	if err := r.Ctx.Err(); err != nil {
		return 0, err
	}
	return r.R.Read(p)
}

// validateProtectedObjects checks the exact loose-object closure, not merely
// ref text or existence. No Git executable, packs, alternates or hooks run.
// Fresh validation is required before recovery retires an integration claim.
func Validate(ctx context.Context, root *os.Root, commit string) error {
	if root == nil || !commitPattern.MatchString(commit) {
		return failure.ErrHold
	}
	todo := []Link{{commit, "commit"}}
	seen := make(map[string]string)
	remaining := MaxBytes
	for len(todo) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		link := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if prior, ok := seen[link.OID]; ok {
			if prior != link.Kind {
				return failure.ErrHold
			}
			continue
		}
		if len(seen) >= MaxCount || !commitPattern.MatchString(link.OID) {
			return failure.ErrHold
		}
		kind, payload, size, err := Read(ctx, root, link.OID, remaining)
		if err != nil || kind != link.Kind {
			return failure.ErrHold
		}
		remaining -= size
		seen[link.OID] = kind
		links, err := Links(kind, payload)
		if err != nil || len(todo)+len(links) > MaxCount {
			return failure.ErrHold
		}
		todo = append(todo, links...)
	}
	return nil
}

func Read(ctx context.Context, root *os.Root, oid string, budget int64) (string, []byte, int64, error) {
	if !commitPattern.MatchString(oid) || budget < 0 {
		return "", nil, 0, failure.ErrHold
	}
	f, err := root.Open("objects/" + oid[:2] + "/" + oid[2:])
	if err != nil {
		return "", nil, 0, failure.ErrHold
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 65<<20 {
		return "", nil, 0, failure.ErrHold
	}
	compressed := bufio.NewReader(ContextReader{ctx, f})
	zr, err := zlib.NewReader(compressed)
	if err != nil {
		return "", nil, 0, failure.ErrHold
	}
	defer zr.Close()
	r := bufio.NewReader(zr)
	header := make([]byte, 0, 64)
	for len(header) < 64 {
		b, err := r.ReadByte()
		if err != nil {
			return "", nil, 0, failure.ErrHold
		}
		header = append(header, b)
		if b == 0 {
			break
		}
	}
	if len(header) == 0 || header[len(header)-1] != 0 {
		return "", nil, 0, failure.ErrHold
	}
	parts := strings.Split(string(header[:len(header)-1]), " ")
	if len(parts) != 2 {
		return "", nil, 0, failure.ErrHold
	}
	kind := parts[0]
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != parts[1] || n > 64<<20 || n > budget {
		return "", nil, 0, failure.ErrHold
	}
	if kind != "blob" && kind != "tree" && kind != "commit" {
		return "", nil, 0, failure.ErrHold
	}
	if (kind == "tree" && n > 8<<20) || (kind == "commit" && n > 1<<20) {
		return "", nil, 0, failure.ErrHold
	}
	h := sha1.New()
	h.Write(header)
	var payload bytes.Buffer
	dst := io.Writer(h)
	if kind != "blob" {
		dst = io.MultiWriter(h, &payload)
	}
	got, err := io.CopyBuffer(dst, io.LimitReader(ContextReader{ctx, r}, n+1), make([]byte, 32768))
	if err != nil || got != n {
		return "", nil, 0, failure.ErrHold
	}
	if hex.EncodeToString(h.Sum(nil)) != oid {
		return "", nil, 0, failure.ErrHold
	}
	if _, err := compressed.ReadByte(); err != io.EOF {
		return "", nil, 0, failure.ErrHold
	}
	return kind, payload.Bytes(), n, nil
}

func Links(kind string, payload []byte) ([]Link, error) {
	if kind == "blob" {
		return nil, nil
	}
	var links []Link
	if kind == "commit" {
		end := bytes.Index(payload, []byte("\n\n"))
		if end < 0 {
			return nil, failure.ErrHold
		}
		lines := strings.Split(string(payload[:end]), "\n")
		if len(lines) < 1 || !strings.HasPrefix(lines[0], "tree ") || !commitPattern.MatchString(strings.TrimPrefix(lines[0], "tree ")) {
			return nil, failure.ErrHold
		}
		links = append(links, Link{lines[0][5:], "tree"})
		for _, line := range lines[1:] {
			if strings.HasPrefix(line, "tree ") {
				return nil, failure.ErrHold
			}
			if strings.HasPrefix(line, "parent ") {
				oid := strings.TrimPrefix(line, "parent ")
				if !commitPattern.MatchString(oid) {
					return nil, failure.ErrHold
				}
				links = append(links, Link{oid, "commit"})
			}
		}
		return links, nil
	}
	if kind != "tree" {
		return nil, failure.ErrHold
	}
	names := make(map[string]bool)
	for len(payload) > 0 {
		zero := bytes.IndexByte(payload, 0)
		if zero < 0 || zero > 4096 || len(payload) < zero+21 {
			return nil, failure.ErrHold
		}
		entry := payload[:zero]
		space := bytes.IndexByte(entry, ' ')
		if space < 1 {
			return nil, failure.ErrHold
		}
		mode, name := string(entry[:space]), string(entry[space+1:])
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || names[name] {
			return nil, failure.ErrHold
		}
		names[name] = true
		oid := hex.EncodeToString(payload[zero+1 : zero+21])
		switch mode {
		case "40000":
			links = append(links, Link{oid, "tree"})
		case "100644", "100755", "120000":
			links = append(links, Link{oid, "blob"})
		case "160000": // A gitlink references a separate repository, not this closure.
		default:
			return nil, failure.ErrHold
		}
		payload = payload[zero+21:]
		if len(links) > MaxCount {
			return nil, failure.ErrHold
		}
	}
	return links, nil
}
