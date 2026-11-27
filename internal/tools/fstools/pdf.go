package fstools

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/tooldef"
	"path/filepath"
)

type PDFReadInput struct {
	Path   string `json:"path"`
	Pages  string `json:"pages,omitempty" jsonschema:"Optional 1-based page N or range N-M."`
	Cursor int    `json:"cursor,omitempty" jsonschema:"UTF-8 byte cursor from next_cursor; at most 64 KiB per reply."`
}
type PDFWriteInput struct {
	Path           string `json:"path"`
	Title          string `json:"title,omitempty"`
	Text           string `json:"text,omitempty"`
	Markdown       string `json:"markdown,omitempty" jsonschema:"Exactly one of text or markdown; headings, paragraphs, lists and fenced code, up to 64 KiB including title."`
	Append         bool   `json:"append,omitempty" jsonschema:"Append generated pages using pdfunite; requires expected_sha256."`
	ExpectedSHA256 string `json:"expected_sha256,omitempty" jsonschema:"Required to replace or append to an existing file; omit to create without overwrite."`
	LeaseToken     string `json:"lease_token,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

func registerPDF(add func(tooldef.Def), d Deps) {
	closed := false
	add(tooldef.New(&mcp.Tool{Name: "pdf_read", Description: "Read PDF text via /usr/bin/pdftotext under fs_read path policy, with shared secret filtering. Optional pages N or N-M and UTF-8 next_cursor; max 64 KiB per reply, 4 MiB extracted text, 10 seconds. No OCR.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in PDFReadInput) (*mcp.CallToolResult, filesystem.PDFReadResult, error) {
		out, err := d.Files.ReadPDF(ctx, in.Path, in.Pages, in.Cursor)
		return nil, out, err
	}))
	add(tooldef.New(&mcp.Tool{Name: "pdf_write", Description: "Create a simple PDF from text or Markdown using Go and embedded DejaVu Sans/Mono TrueType fonts (Cyrillic). Same paths, leases and atomic publication as fs_write; no overwrite without expected_sha256. Optional append uses /usr/bin/pdfunite and requires the original hash. Up to 64 KiB input, 128 pages, 4 MiB PDF.", Annotations: &mcp.ToolAnnotations{IdempotentHint: true, OpenWorldHint: &closed}}, func(ctx context.Context, req *mcp.CallToolRequest, in PDFWriteInput) (*mcp.CallToolResult, filesystem.AtomicWriteResult, error) {
		payload := struct {
			Path, Title, Text, Markdown, ExpectedSHA256, Holder string
			Append                                              bool
		}{in.Path, in.Title, in.Text, in.Markdown, in.ExpectedSHA256, HolderTokenDigest(in.LeaseToken), in.Append}
		out, err := idempotency.Run(d.Store, in.IdempotencyKey, "pdf_write", payload, func() (filesystem.AtomicWriteResult, error) {
			var result filesystem.AtomicWriteResult
			err := d.withWrite([]string{in.Path}, in.LeaseToken, func() error {
				anchor, missing := filesystem.ExistingAnchor(filepath.Clean(in.Path))
				if len(missing) > 0 {
					if err := d.Files.CanCreateIn(anchor); err != nil {
						return err
					}
				}
				if err := filesystem.MakeMissing(missing); err != nil {
					return err
				}
				var err error
				result, err = d.Files.WritePDF(ctx, in.Path, in.Title, in.Text, in.Markdown, in.ExpectedSHA256, in.Append)
				if err != nil {
					filesystem.RemoveMissing(missing)
				}
				return err
			})
			return result, err
		})
		return nil, out, err
	}))
}
