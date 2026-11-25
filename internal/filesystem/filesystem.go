package filesystem

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"mcpshka/internal/audit"
	"mcpshka/internal/policy"
	"mcpshka/internal/trust"
)

var errResultLimit = errors.New("result limit reached")

const (
	DefaultReadLimit  = 16 * 1024
	MaxReadLimit      = 64 * 1024
	DefaultListLimit  = 200
	MaxListLimit      = 1000
	DefaultFindLimit  = 50
	MaxFindLimit      = 200
	MaxSearchFileSize = 4 * 1024 * 1024
	MaxPatchFileSize  = 4 * 1024 * 1024
	MaxHashFileSize   = 16 * 1024 * 1024
)

type Service struct {
	policy *policy.Policy
	audit  *audit.Logger
}

func New(p *policy.Policy) *Service {
	return NewWithAudit(p, nil)
}

func NewWithAudit(p *policy.Policy, logger *audit.Logger) *Service {
	if p == nil {
		p = policy.Default()
	}
	return &Service{policy: p, audit: logger}
}

type Entry struct {
	Path    string    "json:\"path\""
	Type    string    "json:\"type\""
	Size    int64     "json:\"size,omitempty\""
	Mode    string    "json:\"mode,omitempty\""
	ModTime time.Time "json:\"mod_time,omitempty\""
}

type ListResult struct {
	Trust     trust.Label `json:"trust"`
	Root      string      "json:\"root\""
	Entries   []Entry     "json:\"entries\""
	Truncated bool        "json:\"truncated\""
}

func (s *Service) List(path string, depth, maxEntries int) (ListResult, error) {
	root, err := s.policy.ResolveExisting(path)
	if err != nil {
		return ListResult{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return ListResult{}, err
	}
	if !info.IsDir() {
		return ListResult{}, fmt.Errorf("not a directory: %s", root)
	}
	if depth <= 0 {
		depth = 1
	}
	if depth > 4 {
		depth = 4
	}
	if maxEntries <= 0 {
		maxEntries = DefaultListLimit
	}
	if maxEntries > MaxListLimit {
		maxEntries = MaxListLimit
	}

	result := ListResult{Trust: trust.Untrusted, Root: root}
	var walk func(string, int) error
	walk = func(dir string, level int) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, de := range entries {
			if len(result.Entries) >= maxEntries {
				result.Truncated = true
				return nil
			}
			full := filepath.Join(dir, de.Name())
			info, err := os.Lstat(full)
			if err != nil {
				return err
			}
			typ := "file"
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				typ = "symlink"
			case info.IsDir():
				typ = "directory"
			case !info.Mode().IsRegular():
				typ = "other"
			}
			result.Entries = append(result.Entries, Entry{
				Path: full, Type: typ, Size: info.Size(),
				Mode: info.Mode().String(), ModTime: info.ModTime().UTC(),
			})
			if typ == "directory" && level < depth {
				if err := walk(full, level+1); err != nil {
					return err
				}
				if result.Truncated {
					return nil
				}
			}
		}
		return nil
	}
	if err := walk(root, 1); err != nil {
		return ListResult{}, err
	}
	return result, nil
}

type ReadResult struct {
	Trust         trust.Label `json:"trust"`
	Path          string      "json:\"path\""
	Encoding      string      "json:\"encoding\""
	Text          string      "json:\"text,omitempty\""
	Base64        string      "json:\"base64,omitempty\""
	Cursor        int64       "json:\"cursor\""
	NextCursor    int64       "json:\"next_cursor\""
	TotalBytes    int64       "json:\"total_bytes\""
	Truncated     bool        "json:\"truncated\""
	HasEarlier    bool        "json:\"has_earlier\""
	SHA256        string      "json:\"sha256,omitempty\""
	HashAvailable bool        "json:\"hash_available\""
	Files         []BatchFile `json:"files,omitempty"` // batch read (ReadMany)
}

func (s *Service) Read(path string, cursor int64, limit int, tail bool) (ReadResult, error) {
	real, err := s.policy.ResolveExisting(path)
	if err != nil {
		return ReadResult{}, err
	}
	f, err := os.Open(real)
	if err != nil {
		return ReadResult{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ReadResult{}, err
	}
	if !info.Mode().IsRegular() {
		return ReadResult{}, fmt.Errorf("not a regular file: %s", real)
	}
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	if limit > MaxReadLimit {
		limit = MaxReadLimit
	}
	total := info.Size()
	if tail {
		cursor = total - int64(limit)
		if cursor < 0 {
			cursor = 0
		}
	}
	if cursor < 0 {
		return ReadResult{}, errors.New("cursor must be >= 0")
	}
	if cursor > total {
		cursor = total
	}
	if _, err := f.Seek(cursor, io.SeekStart); err != nil {
		return ReadResult{}, err
	}
	buf := make([]byte, limit)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return ReadResult{}, err
	}
	buf = buf[:n]
	// A byte window may cut a UTF-8 character at either edge (Cyrillic is two
	// bytes): drop those 1–3 bytes so text stays text instead of turning the
	// whole window into base64; the cursors say exactly what was returned.
	if !utf8.Valid(buf) {
		lead := 0
		for lead < 3 && lead < len(buf) && cursor > 0 && !utf8.RuneStart(buf[lead]) {
			lead++
		}
		end := len(buf)
		if next := cursor + int64(n); next < total {
			for cut := 1; cut <= 3 && end-cut >= lead; cut++ {
				if utf8.RuneStart(buf[end-cut]) {
					if !utf8.FullRune(buf[end-cut:]) {
						end -= cut
					}
					break
				}
			}
		}
		if trimmed := buf[lead:end]; utf8.Valid(trimmed) {
			cursor += int64(lead)
			buf = trimmed
			n = len(buf)
		}
	}
	next := cursor + int64(n)
	out := ReadResult{
		Trust: trust.Untrusted, Path: real, Cursor: cursor, NextCursor: next, TotalBytes: total,
		Truncated: next < total, HasEarlier: cursor > 0,
	}
	if utf8.Valid(buf) {
		out.Encoding = "utf-8"
		out.Text = string(buf)
	} else {
		out.Encoding = "base64"
		out.Base64 = base64.StdEncoding.EncodeToString(buf)
	}
	if total <= MaxHashFileSize {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return ReadResult{}, err
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return ReadResult{}, err
		}
		out.SHA256 = hex.EncodeToString(h.Sum(nil))
		out.HashAvailable = true
	}
	return out, nil
}

type PatchResult struct {
	Path         string "json:\"path\""
	Replacements int    "json:\"replacements\""
	OldSHA256    string "json:\"old_sha256\""
	NewSHA256    string "json:\"new_sha256\""
	Bytes        int    "json:\"bytes\""
}

func (s *Service) Patch(path, oldText, newText, expectedSHA256 string, expectedReplacements int) (PatchResult, error) {
	if oldText == "" {
		return PatchResult{}, errors.New("old_text must not be empty")
	}
	if expectedSHA256 == "" {
		return PatchResult{}, errors.New("expected_sha256 is required")
	}
	if expectedReplacements <= 0 {
		expectedReplacements = 1
	}
	real, err := s.policy.ResolveExisting(path)
	if err != nil {
		return PatchResult{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return PatchResult{}, err
	}
	if !info.Mode().IsRegular() {
		return PatchResult{}, fmt.Errorf("not a regular file: %s", real)
	}
	if info.Size() > MaxPatchFileSize {
		return PatchResult{}, fmt.Errorf("file exceeds patch limit of %d bytes", MaxPatchFileSize)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return PatchResult{}, err
	}
	if !utf8.Valid(data) {
		return PatchResult{}, errors.New("fs_patch only supports UTF-8 text files")
	}
	oldHashBytes := sha256.Sum256(data)
	oldHash := hex.EncodeToString(oldHashBytes[:])
	if !strings.EqualFold(oldHash, expectedSHA256) {
		return PatchResult{}, fmt.Errorf("sha256 mismatch: current=%s", oldHash)
	}
	count := bytes.Count(data, []byte(oldText))
	if count != expectedReplacements {
		return PatchResult{}, patchMiss(data, oldText, count, expectedReplacements)
	}
	next := bytes.Replace(data, []byte(oldText), []byte(newText), expectedReplacements)
	newHashBytes := sha256.Sum256(next)
	newHash := hex.EncodeToString(newHashBytes[:])

	tmp, err := os.CreateTemp(filepath.Dir(real), ".mcpshka-patch-*")
	if err != nil {
		return PatchResult{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return PatchResult{}, err
	}
	if _, err := tmp.Write(next); err != nil {
		_ = tmp.Close()
		return PatchResult{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return PatchResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return PatchResult{}, err
	}
	if err := os.Rename(tmpName, real); err != nil {
		return PatchResult{}, err
	}
	if dir, err := os.Open(filepath.Dir(real)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	result := PatchResult{
		Path: real, Replacements: count, OldSHA256: oldHash,
		NewSHA256: newHash, Bytes: len(next),
	}
	if s.audit != nil {
		_ = s.audit.Log(audit.Event{
			Type: "fs_patch", Result: "done",
			Details: map[string]any{
				"path": real, "replacements": count,
				"old_sha256": oldHash, "new_sha256": newHash,
			},
		})
	}
	return result, nil
}

// StatPath returns bounded metadata without following the last component.
func (s *Service) StatPath(path string) (Entry, error) {
	if path == "" {
		return Entry{}, errors.New("path required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Entry{}, err
	}
	parent, err := s.policy.CheckWorkdir(filepath.Dir(abs))
	if err != nil {
		if missing, ok := s.missingUnderAllowed(abs); ok {
			return missing, nil
		}
		if _, rootErr := s.policy.ResolveExisting(abs); rootErr != nil {
			return Entry{}, err
		}
		parent = filepath.Dir(abs)
	}
	real := filepath.Join(parent, filepath.Base(abs))
	info, err := os.Lstat(real)
	if os.IsNotExist(err) {
		return Entry{Path: real, Type: "missing"}, nil
	}
	if err != nil {
		return Entry{}, err
	}
	kind := "other"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
	case info.IsDir():
		kind = "directory"
	case info.Mode().IsRegular():
		kind = "file"
	}
	return Entry{Path: real, Type: kind, Size: info.Size(), Mode: info.Mode().String(), ModTime: info.ModTime().UTC()}, nil
}

type AtomicWriteResult struct {
	Path      string `json:"path"`
	Created   bool   `json:"created"`
	OldSHA256 string `json:"old_sha256,omitempty"`
	NewSHA256 string `json:"new_sha256"`
	Bytes     int    `json:"bytes"`
}

// AtomicWrite creates a new file or replaces an existing regular file only
// when its observed SHA matches. It never accepts symlink endpoints.
func (s *Service) AtomicWrite(path, encoded, expectedSHA string) (AtomicWriteResult, error) {
	if len(encoded) == 0 || len(encoded) > 90000 {
		return AtomicWriteResult{}, errors.New("bounded base64 content required")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > MaxReadLimit {
		return AtomicWriteResult{}, errors.New("invalid content or file exceeds 64 KiB")
	}
	return s.atomicWriteData(path, data, expectedSHA)
}

// AtomicAppend publishes prev+chunk atomically (fs_write append=true): the
// way to build a file larger than one 64 KiB call, as RDC write_file does
// with mode append. expected_sha256 of the current file is required and
// re-checked under the write; the result stays within 4 MiB.
func (s *Service) AtomicAppend(path, encoded, expectedSHA string) (AtomicWriteResult, error) {
	if expectedSHA == "" {
		return AtomicWriteResult{}, errors.New("append requires expected_sha256 from fs_read or the previous fs_write")
	}
	if len(encoded) == 0 || len(encoded) > 90000 {
		return AtomicWriteResult{}, errors.New("bounded base64 content required")
	}
	chunk, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(chunk) == 0 || len(chunk) > MaxReadLimit {
		return AtomicWriteResult{}, errors.New("invalid content or chunk exceeds 64 KiB")
	}
	real, err := s.policy.ResolveExisting(path)
	if err != nil {
		return AtomicWriteResult{}, err
	}
	info, err := os.Lstat(real)
	if err != nil {
		return AtomicWriteResult{}, err
	}
	if !info.Mode().IsRegular() || info.Size()+int64(len(chunk)) > MaxPatchFileSize {
		return AtomicWriteResult{}, errors.New("append target must be a regular file staying within 4 MiB")
	}
	prev, err := os.ReadFile(real)
	if err != nil {
		return AtomicWriteResult{}, err
	}
	return s.atomicWriteData(path, append(prev, chunk...), expectedSHA)
}

// atomicWriteData shares the fs_write publication and path checks with PDF output.
func (s *Service) atomicWriteData(path string, data []byte, expectedSHA string) (AtomicWriteResult, error) {
	if len(data) == 0 || len(data) > MaxPatchFileSize {
		return AtomicWriteResult{}, errors.New("file exceeds 4 MiB")
	}
	var err error
	if expectedSHA != "" {
		if len(expectedSHA) != 64 {
			return AtomicWriteResult{}, errors.New("expected_sha256 must be a SHA-256 hex digest")
		}
		if _, err = hex.DecodeString(expectedSHA); err != nil {
			return AtomicWriteResult{}, err
		}
	}
	real, err := s.policy.ResolveForWrite(path)
	if err != nil {
		return AtomicWriteResult{}, err
	}
	original, err := os.Lstat(path)
	if err == nil && (!original.Mode().IsRegular() || original.Mode()&os.ModeSymlink != 0) {
		return AtomicWriteResult{}, errors.New("destination must be a regular non-symlink file")
	}
	if err != nil && !os.IsNotExist(err) {
		return AtomicWriteResult{}, err
	}
	created := os.IsNotExist(err)
	oldSHA := ""
	mode := os.FileMode(0600)
	if created {
		if expectedSHA != "" {
			return AtomicWriteResult{}, errors.New("expected existing file is absent")
		}
	} else {
		if expectedSHA == "" || original.Size() > MaxPatchFileSize {
			return AtomicWriteResult{}, errors.New("expected_sha256 required to replace bounded file")
		}
		prev, readErr := os.ReadFile(real)
		if readErr != nil {
			return AtomicWriteResult{}, readErr
		}
		sum := sha256.Sum256(prev)
		oldSHA = hex.EncodeToString(sum[:])
		if !strings.EqualFold(oldSHA, expectedSHA) {
			return AtomicWriteResult{}, errors.New("SHA mismatch: HOLD")
		}
		mode = original.Mode().Perm()
	}
	temp, err := os.CreateTemp(filepath.Dir(real), ".mcpshka-atomic-*")
	if err != nil {
		return AtomicWriteResult{}, err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(mode); err != nil {
		temp.Close()
		return AtomicWriteResult{}, err
	}
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return AtomicWriteResult{}, err
	}
	if closeErr != nil {
		return AtomicWriteResult{}, closeErr
	}
	if created {
		// Publish by link, never clobber an independently created file.
		if err = os.Link(temp.Name(), real); err != nil {
			return AtomicWriteResult{}, err
		}
	} else {
		fresh, statErr := os.Lstat(real)
		if statErr != nil || !fresh.Mode().IsRegular() || !os.SameFile(original, fresh) {
			return AtomicWriteResult{}, errors.New("file identity changed: HOLD")
		}
		now, readErr := os.ReadFile(real)
		if readErr != nil {
			return AtomicWriteResult{}, readErr
		}
		sum := sha256.Sum256(now)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), expectedSHA) {
			return AtomicWriteResult{}, errors.New("file changed during write: HOLD")
		}
		if err = os.Rename(temp.Name(), real); err != nil {
			return AtomicWriteResult{}, err
		}
	}
	d, err := os.Open(filepath.Dir(real))
	if err != nil {
		return AtomicWriteResult{}, err
	}
	err = d.Sync()
	_ = d.Close()
	if err != nil {
		return AtomicWriteResult{}, fmt.Errorf("published but directory sync unknown: %w", err)
	}
	sum := sha256.Sum256(data)
	out := AtomicWriteResult{Path: real, Created: created, OldSHA256: oldSHA, NewSHA256: hex.EncodeToString(sum[:]), Bytes: len(data)}
	if s.audit != nil {
		_ = s.audit.Log(audit.Event{Type: "fs_write", Result: "done", Details: map[string]any{"path": real, "created": created, "old_sha256": oldSHA, "new_sha256": out.NewSHA256}})
	}
	return out, nil
}
