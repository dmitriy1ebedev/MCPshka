package memory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mcpshka/internal/knowledge"
	"mcpshka/internal/runoutcome"
)

// Session archive sync (ROADMAP S4.K2.4). Claude Code and Codex keep every
// session as JSONL under root's home; the root CLI (memory-sync, run by the
// session hook) reads them where they are — nothing is copied — and adds
// only the owner's words and the AI's last answer of each turn, secrets
// masked, as pending inbox chunks. Each file keeps its byte offset in
// mem_inbox_seen: a JSONL only grows, so a rerun or an interrupted sync
// continues from there and never duplicates a chunk. Distillation stays with
// the calling AI (memory_recall inbox=true); no model runs here.

const (
	sessionChunk = 3000     // chars of one session chunk (owner words + turn summaries)
	syncBudget   = 64 << 20 // bytes read per run; the next run continues
)

const seenSchema = "CREATE TABLE IF NOT EXISTS mem_inbox_seen(file TEXT PRIMARY KEY, offset INTEGER NOT NULL, at TEXT NOT NULL);\n"

// SyncReport is one sync run.
type SyncReport struct {
	runoutcome.Result
	Files     int         `json:"files"`
	Chunks    int         `json:"chunks"`
	Bytes     int64       `json:"bytes"`
	Remaining bool        `json:"remaining,omitempty"` // budget reached: run again
	Inbox     InboxReport `json:"inbox"`
}

type sessionLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   *sessionMessage `json:"message"`
	Payload   *sessionMessage `json:"payload"`
}

type sessionMessage struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// turnText reads one JSONL line of either tool: user and assistant text only;
// tool calls, tool results and injected context are not the owner's words.
func turnText(raw []byte) (role, text string, at time.Time) {
	var l sessionLine
	if json.Unmarshal(raw, &l) != nil {
		return "", "", at
	}
	at, _ = time.Parse(time.RFC3339Nano, l.Timestamp)
	m := l.Message
	if l.Type == "response_item" {
		m = l.Payload
		if m == nil || m.Type != "message" {
			return "", "", at
		}
	} else if l.Type != "user" && l.Type != "assistant" {
		return "", "", at
	}
	if m == nil || (m.Role != "user" && m.Role != "assistant") {
		return "", "", at
	}
	var s string
	if json.Unmarshal(m.Content, &s) != nil {
		var parts []struct{ Type, Text string }
		if json.Unmarshal(m.Content, &parts) != nil {
			return "", "", at
		}
		var b []string
		for _, p := range parts {
			switch p.Type {
			case "text", "input_text", "output_text":
				b = append(b, p.Text)
			case "tool_result", "tool_use":
				return "", "", at
			}
		}
		s = strings.Join(b, "\n")
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "<") {
		return "", "", at // <environment_context>, <system-reminder>, command echoes
	}
	return m.Role, s, at
}

// SyncSessions adds new turns of every *.jsonl under roots to the inbox.
func (s *Store) SyncSessions(ctx context.Context, roots []string) (rep SyncReport, runErr error) {
	input := false
	blocker, next := "", ""
	defer func() {
		if runErr != nil && blocker == "" {
			blocker, next = "session_sync_failed", "inspect the sync error before retrying"
		}
		rep.Result = runoutcome.Resolve(rep.Chunks, input, blocker, next)
	}()
	if _, err := s.exec(ctx, inboxSchema+"\n"+seenSchema); err != nil {
		return rep, err
	}
	rows, err := s.exec(ctx, "SELECT file, offset FROM mem_inbox_seen;")
	if err != nil {
		return rep, err
	}
	seen := map[string]int64{}
	for _, r := range rows {
		n, _ := r["offset"].(float64)
		seen[str(r, "file")] = int64(n)
	}
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() && strings.HasSuffix(p, ".jsonl") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			blocker, next = "session_source_unreadable", "restore archive directory access and rerun memory-sync"
			continue
		}
	}
	sort.Strings(files)
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			blocker, next = "session_file_unreadable", "check session file before retrying memory-sync"
			continue
		}
		if info.Size() < seen[f] {
			blocker, next = "session_cursor_past_end", "inspect archive truncation and its persisted offset"
			continue
		}
		if info.Size() == seen[f] {
			continue
		}
		input = true
		if rep.Bytes >= syncBudget {
			rep.Remaining = true
			break
		}
		n, read, err := s.syncFile(ctx, f, seen[f])
		if err != nil {
			blocker, next = "session_file_read_or_commit_failed", "inspect the file and persisted offset before retrying"
			return rep, err
		}
		if n == 0 && blocker == "" {
			blocker, next = "session_cursor_has_no_new_complete_turns", "wait for a complete turn or inspect the skipped input"
		}
		rep.Files++
		rep.Chunks += n
		rep.Bytes += read
	}
	if rep.Chunks > 0 && blocker == "session_cursor_has_no_new_complete_turns" {
		blocker, next = "", ""
	}
	rep.Inbox, err = s.Inbox(ctx)
	return rep, err
}

// syncFile reads complete lines from offset, turns them into chunks and
// stores chunks and the new offset in one transaction.
func (s *Store) syncFile(ctx context.Context, file string, offset int64) (int, int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, 0, err
	}
	r := bufio.NewReaderSize(io.LimitReader(f, syncBudget), 1<<20)
	var chunks []string
	var b strings.Builder
	var first time.Time
	pending := "" // last assistant text of the current turn
	emit := func(line string) {
		line = knowledge.MaskSecrets(line)
		if len(line) > sessionChunk {
			line = truncate(line, sessionChunk)
		}
		if b.Len()+len(line) > sessionChunk && b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
		}
		b.WriteString(line)
	}
	read := int64(0)
	for {
		raw, err := r.ReadBytes('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return 0, 0, err
			}
			break // a partial last line is read again next time
		}
		read += int64(len(raw))
		role, text, at := turnText(raw)
		if role == "" {
			continue
		}
		if first.IsZero() {
			first = at
		}
		if role == "assistant" {
			pending = text
			continue
		}
		if pending != "" {
			emit("assistant: " + pending + "\n")
			pending = ""
		}
		emit("owner: " + text + "\n")
	}
	if read == 0 {
		return 0, 0, nil
	}
	if pending != "" {
		emit("assistant: " + pending + "\n")
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	if first.IsZero() {
		first = s.now()
	}
	name := filepath.Base(filepath.Dir(file)) + "/" + strings.TrimSuffix(filepath.Base(file), ".jsonl")
	var q strings.Builder
	q.WriteString("BEGIN IMMEDIATE;\nCREATE TEMP TABLE session_fence(ok INTEGER CHECK(ok=1));\n")
	fmt.Fprintf(&q, "INSERT INTO session_fence SELECT coalesce((SELECT offset FROM mem_inbox_seen WHERE file=%s),0)=%d;\n", lit(file), offset)
	for i, text := range chunks {
		sum := sha256.Sum256([]byte(text))
		id := "chat:" + hex.EncodeToString(sum[:8])
		title := clean(fmt.Sprintf("сессия %s %s (%d)", name, first.Format("2006-01-02"), i+1), 200)
		q.WriteString("INSERT OR IGNORE INTO mem_inbox(id,file,title,text,at) VALUES(" + lit(id) + "," + lit("session:"+file) + "," + lit(title) + "," + lit(text) + "," + litTime(&first) + ");\n")
	}
	now := s.now().UTC()
	q.WriteString(fmt.Sprintf("INSERT INTO mem_inbox_seen(file,offset,at) VALUES(%s,%d,%s) ON CONFLICT(file) DO UPDATE SET offset=excluded.offset, at=excluded.at;\nCOMMIT;\nSELECT total_changes()-2 AS inserted;", lit(file), offset+read, litTime(&now)))
	rows, err := s.execFor(ctx, 30*time.Second, q.String())
	if err != nil {
		return 0, 0, err
	}
	n, _ := rows[0]["inserted"].(float64)
	return int(n), read, nil
}
