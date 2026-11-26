package memory

// Chat archive distillation (0.11.3 item 8). A ChatGPT or Claude export put
// into memory-inbox (the «Память» page or a copy) is cut into conversation
// chunks; an AI in any chat takes the next pending chunks with memory_recall
// inbox=true, writes 0–5 distilled records with memory_write (source = the
// chunk id) and the chunk counts as distilled; an empty chunk is skipped with
// memory_write inbox_skip. Coverage: pending / distilled / skipped and the
// records made. The export itself is never deleted here.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/knowledge"
	"mcpshka/internal/runoutcome"
)

const (
	inboxChunk = 6000 // chars of one chunk: one AI turn distils it
	inboxDir   = "memory-inbox"
)

const inboxSchema = `CREATE TABLE IF NOT EXISTS mem_inbox(id TEXT PRIMARY KEY, file TEXT NOT NULL, title TEXT NOT NULL,
 text TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending', made INTEGER NOT NULL DEFAULT 0, at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS mem_inbox_state ON mem_inbox(state);`

// InboxReport is the distillation coverage.
type InboxReport struct {
	runoutcome.Result
	Files     int `json:"files,omitempty"`
	Pending   int `json:"pending"`
	Distilled int `json:"distilled"`
	Skipped   int `json:"skipped"`
	Records   int `json:"records"`
}

// chatFormat detects a ChatGPT (mapping) or Claude (chat_messages) export.
func chatFormat(raw []byte) string {
	head := raw[:min(len(raw), 1<<16)]
	switch {
	case bytes.Contains(head, []byte(`"mapping"`)):
		return "chatgpt"
	case bytes.Contains(head, []byte(`"chat_messages"`)):
		return "claude"
	}
	return ""
}

// exportJSON returns conversations.json of a zip export or the file itself.
func exportJSON(file string) ([]byte, error) {
	if strings.HasSuffix(strings.ToLower(file), ".zip") {
		zr, err := zip.OpenReader(file)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if filepath.Base(f.Name) == "conversations.json" && f.UncompressedSize64 <= knowledge.MaxChatBytes {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, knowledge.MaxChatBytes))
			}
		}
		return nil, errors.New("memory inbox: no conversations.json in " + filepath.Base(file))
	}
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if info.Size() > knowledge.MaxChatBytes {
		return nil, fmt.Errorf("memory inbox: %s is over %d MB", filepath.Base(file), knowledge.MaxChatBytes>>20)
	}
	return os.ReadFile(file)
}

// InboxSplit cuts one export into pending chunks; chunks already known (same
// text) are kept as they are, so a re-upload adds only new conversations.
func (s *Store) InboxSplit(ctx context.Context, file string) (int, error) {
	if _, err := s.exec(ctx, inboxSchema); err != nil {
		return 0, err
	}
	raw, err := exportJSON(file)
	if err != nil {
		return 0, err
	}
	format := chatFormat(raw)
	if format == "" {
		return 0, fmt.Errorf("memory inbox: %s is not a ChatGPT or Claude export", filepath.Base(file))
	}
	type conv struct {
		title string
		at    time.Time
		b     strings.Builder
		parts []string
	}
	convs := map[string]*conv{}
	order := []string{}
	flush := func(c *conv) {
		if c.b.Len() > 0 {
			c.parts = append(c.parts, c.b.String())
			c.b.Reset()
		}
	}
	err = knowledge.ParseChat(bytes.NewReader(raw), format, func(title, role, text string, at time.Time) error {
		c := convs[title]
		if c == nil {
			c = &conv{title: title, at: at}
			convs[title] = c
			order = append(order, title)
		}
		line := role + ": " + strings.TrimSpace(knowledge.MaskSecrets(text)) + "\n"
		if c.b.Len()+len(line) > inboxChunk {
			flush(c)
		}
		if len(line) > inboxChunk {
			line = truncate(line, inboxChunk)
		}
		c.b.WriteString(line)
		return nil
	})
	if err != nil {
		return 0, err
	}
	var b strings.Builder
	b.WriteString("BEGIN IMMEDIATE;\n")
	base := filepath.Base(file)
	for _, title := range order {
		c := convs[title]
		flush(c)
		for i, text := range c.parts {
			sum := sha256.Sum256([]byte(text))
			id := "chat:" + hex.EncodeToString(sum[:8])
			t := clean(title, 200)
			if len(c.parts) > 1 {
				t += fmt.Sprintf(" (%d/%d)", i+1, len(c.parts))
			}
			at := c.at
			if at.IsZero() {
				at = s.now()
			}
			b.WriteString("INSERT OR IGNORE INTO mem_inbox(id,file,title,text,at) VALUES(" + lit(id) + "," + lit(base) + "," + lit(t) + "," + lit(text) + "," + litTime(&at) + ");\n")
		}
	}
	b.WriteString("COMMIT;\nSELECT total_changes() AS added;")
	rows, err := s.exec(ctx, b.String())
	if err != nil {
		return 0, err
	}
	n, _ := rows[0]["added"].(float64)
	return int(n), nil
}

// InboxScan splits every export in dir/memory-inbox not split yet.
func (s *Store) InboxScan(ctx context.Context) (InboxReport, error) {
	if _, err := s.exec(ctx, inboxSchema); err != nil {
		return InboxReport{}, err
	}
	rows, err := s.exec(ctx, "SELECT DISTINCT file FROM mem_inbox;")
	if err != nil {
		return InboxReport{}, err
	}
	done := map[string]bool{}
	for _, r := range rows {
		done[str(r, "file")] = true
	}
	dir := filepath.Join(filepath.Dir(s.file), inboxDir)
	files, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		r := runoutcome.Resolve(0, true, "inbox_directory_unreadable", "restore read access to the memory inbox directory")
		return InboxReport{Result: r}, err
	}
	var errs []error
	split, added := 0, 0
	input := false
	for _, file := range files {
		f := file.Name()
		ext := strings.ToLower(filepath.Ext(f))
		if file.IsDir() || done[f] || (ext != ".json" && ext != ".zip") {
			continue
		}
		input = true
		n, err := s.InboxSplit(ctx, filepath.Join(dir, f))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		split++
		added += n
	}
	rep, err := s.Inbox(ctx)
	rep.Files = split
	err = errors.Join(append(errs, err)...)
	blocker, next := "", ""
	if err != nil {
		blocker, next = "inbox_export_unreadable_or_invalid", "inspect export read and parse errors before retrying"
	} else if input && added == 0 {
		blocker, next = "inbox_export_has_no_new_chunks", "check whether the export is empty or duplicates existing chunks"
	}
	rep.Result = runoutcome.Resolve(added, input, blocker, next)
	return rep, err
}

// Inbox reports the coverage.
func (s *Store) Inbox(ctx context.Context) (InboxReport, error) {
	rep := InboxReport{}
	rows, err := s.exec(ctx, inboxSchema+"\nSELECT state, count(*) AS n, sum(made) AS made FROM mem_inbox GROUP BY state;")
	for _, r := range rows {
		n, _ := r["n"].(float64)
		made, _ := r["made"].(float64)
		rep.Records += int(made)
		switch str(r, "state") {
		case "pending":
			rep.Pending = int(n)
		case "distilled":
			rep.Distilled = int(n)
		case "skipped":
			rep.Skipped = int(n)
		}
	}
	return rep, err
}

// inboxGuide is what the AI is asked to do with a chunk.
const inboxGuide = "выжми: 0–5 записей memory_write (kind decision|lesson|problem|fact, title, body ≤600: что, почему, итог; questions 5–10 словами владельца; source=этот id); только устойчивое (решения, правила, причины сбоев), без болтовни и секретов; нечего — memory_write inbox_skip=[id]"

// InboxNext returns the oldest pending chunks as hits.
func (s *Store) InboxNext(ctx context.Context, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 2
	}
	limit = min(limit, 10) // session chunks are half size: a batch of ten fits one reply
	rows, err := s.exec(ctx, inboxSchema+fmt.Sprintf("\nSELECT id, title, text, at FROM mem_inbox WHERE state='pending' ORDER BY at, id LIMIT %d;", limit))
	hits := make([]Hit, 0, len(rows))
	for _, r := range rows {
		h := Hit{Record: Record{ID: str(r, "id"), Kind: "chat_excerpt", Title: str(r, "title"), Body: str(r, "text"), Source: str(r, "id")}, Why: inboxGuide}
		if t := parseTime(r, "at"); t != nil {
			h.ValidFrom = *t
		}
		hits = append(hits, h)
	}
	return hits, err
}

// inboxMark records a distilled record of a chunk or skips chunks.
func (s *Store) inboxMark(ctx context.Context, made string, skip []string) error {
	sql := inboxSchema + "\n"
	if strings.HasPrefix(made, "chat:") {
		sql += "UPDATE mem_inbox SET state='distilled', made=made+1 WHERE id=" + lit(made) + ";\n"
	}
	for _, id := range skip {
		sql += "UPDATE mem_inbox SET state='skipped' WHERE id=" + lit(id) + " AND state='pending';\n"
	}
	_, err := s.exec(ctx, sql)
	return err
}

// InboxLine is the brief/MOTD part: chunks waiting for distillation.
func (s *Store) inboxLine(ctx context.Context) string {
	rows, err := s.exec(ctx, "SELECT count(*) AS n FROM sqlite_master WHERE name='mem_inbox';")
	if err != nil || len(rows) != 1 || rows[0]["n"] == float64(0) {
		return ""
	}
	rep, err := s.Inbox(ctx)
	if err != nil || rep.Pending == 0 {
		return ""
	}
	return fmt.Sprintf(" · выжимка чатов: ждут %d, готово %d (%d записей)", rep.Pending, rep.Distilled, rep.Records)
}
