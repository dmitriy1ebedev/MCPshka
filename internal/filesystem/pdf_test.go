package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func requirePDF(t *testing.T) {
	t.Helper()
	for _, p := range []string{"/usr/bin/pdftotext", "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"} {
		if _, e := os.Stat(p); e != nil {
			t.Skip("PDF fixture dependency unavailable:", p)
		}
	}
}
func TestPDFRoundTripAndAppend(t *testing.T) {
	requirePDF(t)
	s, root := testService(t)
	path := filepath.Join(root, "report.pdf")
	ctx := context.Background()
	written, e := s.WritePDF(ctx, path, "Отчёт", "", "# Заголовок\n\nАбзац с кириллицей.\n- Первый пункт\n1. Второй пункт\n\n```go\nfmt.Println(\"Привет\")\n```", "", false)
	if e != nil {
		t.Fatal(e)
	}
	if !written.Created || written.Bytes < 1000 {
		t.Fatalf("write: %+v", written)
	}
	read, e := s.ReadPDF(ctx, path, "", 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"Отчёт", "Заголовок", "Абзац с кириллицей.", "Первый пункт", "Второй пункт", "Привет"} {
		if !strings.Contains(read.Text, want) {
			t.Errorf("missing %q in %q", want, read.Text)
		}
	}
	if read.PageCount != 1 {
		t.Fatalf("pages: %+v", read)
	}
	if _, e = s.WritePDF(ctx, path, "", "new", "", "", false); e == nil {
		t.Fatal("overwrote without hash")
	}
	if _, e = s.WritePDF(ctx, path, "", "new", "", strings.Repeat("0", 64), true); e == nil {
		t.Fatal("appended with stale hash")
	}
	if _, e = os.Stat("/usr/bin/pdfunite"); e != nil {
		t.Skip("pdfunite not installed")
	}
	appended, e := s.WritePDF(ctx, path, "", "Добавленная страница", "", written.NewSHA256, true)
	if e != nil {
		t.Fatal(e)
	}
	if appended.Created {
		t.Fatal("append created instead of replacing")
	}
	second, e := s.ReadPDF(ctx, path, "2", 0)
	if e != nil || !strings.Contains(second.Text, "Добавленная страница") || strings.Contains(second.Text, "Отчёт") {
		t.Fatalf("second page: %+v %v", second, e)
	}
}
func TestPDFReadPaginationFilterAndPaths(t *testing.T) {
	requirePDF(t)
	s, root := testService(t)
	ctx := context.Background()
	path := filepath.Join(root, "large.pdf")
	// Input stays below 64 KiB; pdftotext adds line/page separators beyond it.
	body := strings.Repeat(strings.Repeat("я", 120)+"\n", 271)
	if _, e := s.WritePDF(ctx, path, "", body, "", "", false); e != nil {
		t.Fatal(e)
	}
	first, e := s.ReadPDF(ctx, path, "", 0)
	if e != nil {
		t.Fatal(e)
	}
	if !first.Truncated || len(first.Text) > MaxReadLimit || !utf8.ValidString(first.Text) {
		t.Fatalf("pagination: bytes=%d truncated=%v", len(first.Text), first.Truncated)
	}
	next, e := s.ReadPDF(ctx, path, "", first.NextCursor)
	if e != nil || next.Cursor != first.NextCursor || next.Truncated || !utf8.ValidString(next.Text) {
		t.Fatalf("continuation: %+v %v", next, e)
	}
	whole := first.Text + next.Text
	if len(whole) != first.TotalBytes || strings.Count(whole, "я") != 120*271 {
		t.Fatal("lost or duplicated characters")
	}
	for _, pages := range []string{"0", "2-1", "1;id", "1-2-3"} {
		if _, e = s.ReadPDF(ctx, path, pages, 0); e == nil {
			t.Fatal("accepted pages:", pages)
		}
	}
	if _, e = s.ReadPDF(ctx, path, "", 1); e == nil {
		t.Fatal("accepted middle of UTF-8 rune")
	}
	secret := filepath.Join(root, "secret.pdf")
	if _, e = s.WritePDF(ctx, secret, "", "api_key=supersecretvalue", "", "", false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadPDF(ctx, secret, "", 0); e == nil || !strings.Contains(e.Error(), "secret filter") {
		t.Fatalf("secret leaked: %v", e)
	}
	if _, e = s.ReadPDF(ctx, "/etc/passwd", "", 0); e == nil {
		t.Fatal("read escaped roots")
	}
	if _, e = s.WritePDF(ctx, "/etc/mcpshka-forbidden.pdf", "", "text", "", "", false); e == nil {
		t.Fatal("write escaped roots")
	}
	link := filepath.Join(root, "link.pdf")
	if e = os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e = s.WritePDF(ctx, link, "", "text", "", "", false); e == nil {
		t.Fatal("wrote symlink")
	}
	bad := filepath.Join(root, "bad.pdf")
	if e = os.WriteFile(bad, []byte("not PDF"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadPDF(ctx, bad, "", 0); e == nil {
		t.Fatal("accepted invalid PDF")
	}
}
func TestPDFExtractionBufferBound(t *testing.T) {
	var b pdfBuffer
	if _, e := b.Write(make([]byte, MaxPatchFileSize)); e != nil {
		t.Fatal(e)
	}
	if _, e := b.Write([]byte("x")); e == nil || b.Len() != MaxPatchFileSize {
		t.Fatal("buffer not bounded")
	}
}
