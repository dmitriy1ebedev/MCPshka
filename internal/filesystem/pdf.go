package filesystem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mcpshka/internal/llmgateway"
	"mcpshka/internal/simplepdf"
	"mcpshka/internal/trust"
)

type PDFReadResult struct {
	Trust      trust.Label `json:"trust"`
	Path       string      `json:"path"`
	Text       string      `json:"text"`
	Pages      string      `json:"pages,omitempty"`
	PageCount  int         `json:"page_count"`
	Cursor     int         `json:"cursor"`
	NextCursor int         `json:"next_cursor"`
	TotalBytes int         `json:"total_bytes"`
	Truncated  bool        `json:"truncated"`
}
type pdfBuffer struct{ bytes.Buffer }

func (b *pdfBuffer) Write(p []byte) (int, error) {
	if len(p) > MaxPatchFileSize-b.Len() {
		return 0, errors.New("PDF extraction exceeds 4 MiB; select fewer pages")
	}
	return b.Buffer.Write(p)
}
func pdfPages(pages string) ([]string, error) {
	if pages == "" {
		return nil, nil
	}
	parts := strings.Split(pages, "-")
	if len(parts) > 2 {
		return nil, errors.New("pages must be N or N-M (1-based)")
	}
	first, e := strconv.Atoi(parts[0])
	if e != nil || first < 1 || first > 100000 {
		return nil, errors.New("invalid first PDF page")
	}
	last := first
	if len(parts) == 2 {
		last, e = strconv.Atoi(parts[1])
		if e != nil || last < first || last > 100000 {
			return nil, errors.New("invalid last PDF page")
		}
	}
	return []string{"-f", strconv.Itoa(first), "-l", strconv.Itoa(last)}, nil
}
func (s *Service) openPDF(path string) (*os.File, error) {
	real, e := s.policy.ResolveExisting(path)
	if e != nil {
		return nil, e
	}
	f, e := os.Open(real)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("PDF must be a regular file")
	}
	if info.Size() > 64<<20 {
		f.Close()
		return nil, errors.New("PDF input exceeds 64 MiB")
	}
	return f, nil
}
func (s *Service) ReadPDF(ctx context.Context, path, pages string, cursor int) (PDFReadResult, error) {
	args, e := pdfPages(pages)
	if e != nil {
		return PDFReadResult{}, e
	}
	if cursor < 0 {
		return PDFReadResult{}, errors.New("cursor must be >= 0")
	}
	f, e := s.openPDF(path)
	if e != nil {
		return PDFReadResult{}, e
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args = append(args, "-enc", "UTF-8", "/proc/self/fd/3", "-")
	cmd := exec.CommandContext(ctx, "/usr/bin/pdftotext", args...)
	cmd.ExtraFiles = []*os.File{f}
	var b pdfBuffer
	cmd.Stdout = &b
	if e = cmd.Run(); e != nil {
		return PDFReadResult{}, errors.New("pdftotext failed, timed out or exceeded 4 MiB; verify PDF and select fewer pages")
	}
	if !utf8.Valid(b.Bytes()) {
		return PDFReadResult{}, errors.New("pdftotext returned invalid UTF-8")
	}
	filtered := llmgateway.Filter(b.String())
	if !filtered.Allowed {
		return PDFReadResult{}, errors.New("PDF text rejected by secret filter")
	}
	text := filtered.Text
	total := len(text)
	cursor = min(cursor, total)
	if cursor < total && !utf8.RuneStart(text[cursor]) {
		return PDFReadResult{}, errors.New("cursor must be a UTF-8 boundary; use next_cursor")
	}
	end := min(cursor+MaxReadLimit, total)
	for end < total && !utf8.RuneStart(text[end]) {
		end--
	}
	return PDFReadResult{Trust: trust.Untrusted, Path: f.Name(), Text: text[cursor:end], Pages: pages, PageCount: strings.Count(text, "\f"), Cursor: cursor, NextCursor: end, TotalBytes: total, Truncated: end < total}, nil
}

// WritePDF reuses atomic fs_write publication; append needs the observed hash
// and pdfunite, while generating a new document uses Go alone.
func (s *Service) WritePDF(ctx context.Context, path, title, text, markdown, expectedSHA string, appendPages bool) (AtomicWriteResult, error) {
	data, e := simplepdf.Generate(title, text, markdown)
	if e != nil {
		return AtomicWriteResult{}, e
	}
	if appendPages {
		if expectedSHA == "" {
			return AtomicWriteResult{}, errors.New("append requires expected_sha256 from fs_read")
		}
		f, e := s.openPDF(path)
		if e != nil {
			return AtomicWriteResult{}, e
		}
		defer f.Close()
		old, e := io.ReadAll(io.LimitReader(f, MaxPatchFileSize+1))
		if e != nil || len(old) > MaxPatchFileSize {
			return AtomicWriteResult{}, errors.New("append input exceeds 4 MiB")
		}
		sum := sha256.Sum256(old)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), expectedSHA) {
			return AtomicWriteResult{}, errors.New("SHA mismatch: HOLD")
		}
		dir, e := os.MkdirTemp("", "mcpshka-pdf-*")
		if e != nil {
			return AtomicWriteResult{}, e
		}
		defer os.RemoveAll(dir)
		for name, content := range map[string][]byte{"old.pdf": old, "new.pdf": data} {
			if e = os.WriteFile(filepath.Join(dir, name), content, 0600); e != nil {
				return AtomicWriteResult{}, e
			}
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		output := filepath.Join(dir, "merged.pdf")
		cmd := exec.CommandContext(ctx, "/usr/bin/pdfunite", filepath.Join(dir, "old.pdf"), filepath.Join(dir, "new.pdf"), output)
		if e = cmd.Run(); e != nil {
			return AtomicWriteResult{}, fmt.Errorf("PDF append requires working /usr/bin/pdfunite (10 second limit): %w", e)
		}
		merged, e := os.Open(output)
		if e != nil {
			return AtomicWriteResult{}, e
		}
		defer merged.Close()
		data, e = io.ReadAll(io.LimitReader(merged, MaxPatchFileSize+1))
		if e != nil {
			return AtomicWriteResult{}, e
		}
	}
	return s.atomicWriteData(path, data, expectedSHA)
}
