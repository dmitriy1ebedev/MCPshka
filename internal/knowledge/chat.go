package knowledge

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const MaxChatBytes = 16 << 20
const maxMessages = 2000

var privateBlock = regexp.MustCompile(`(?s)-----BEGIN[^\n]*PRIVATE KEY-----.*?(?:-----END[^\n]*PRIVATE KEY-----|$)`)
var credentialAssignment = regexp.MustCompile(`(?i)(?:password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie)["']?\s*[:=]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}\]]+)`)

func SensitiveField(name string) bool { return looksCredential(name, "") }

var secretToken = regexp.MustCompile(`\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)

func MaskSecrets(s string) string {
	s = privateBlock.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = credentialAssignment.ReplaceAllString(s, "[REDACTED CREDENTIAL]")
	s = credentialValue.ReplaceAllString(s, "[REDACTED CREDENTIAL]")
	return secretToken.ReplaceAllString(s, "[REDACTED TOKEN]")
}

type chatMessage struct {
	CreatedAt string `json:"created_at"`
	UUID      string `json:"uuid"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}
type chatConversation struct {
	CreateTime float64       `json:"create_time"` // ChatGPT: epoch seconds
	CreatedAt  string        `json:"created_at"`  // Claude: RFC3339
	Title      string        `json:"title"`
	Name       string        `json:"name"`
	UUID       string        `json:"uuid"`
	Messages   []chatMessage `json:"chat_messages"`
	Mapping    map[string]struct {
		Message *struct {
			CreateTime float64 `json:"create_time"`
			Author     struct {
				Role string `json:"role"`
			} `json:"author"`
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
		}
	} `json:"mapping"`
}

// chatTime picks the message time, else the conversation time.
func chatTime(msgRFC string, msgEpoch float64, convRFC string, convEpoch float64) time.Time {
	for _, v := range []string{msgRFC, convRFC} {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	for _, v := range []float64{msgEpoch, convEpoch} {
		if v > 0 && v < 1e11 {
			return time.Unix(int64(v), int64((v-float64(int64(v)))*1e9))
		}
	}
	return time.Time{}
}

// ImportChat decodes one conversation at a time; total input, records and message
// size are capped. Imported material is untrusted, private until owner review.
func (s *Store) ImportChat(reader io.Reader, format, source, digest string) ([]string, error) {
	if !safeText(source, 256) || !safeText(digest, 128) {
		return nil, ErrInvalid
	}
	lr := &io.LimitedReader{R: reader, N: MaxChatBytes + 1}
	records := []Record{}
	// MCP-035: records keep the conversation/message time, so search can
	// filter by date; a missing or future time falls back to import time.
	appendText := func(title, role, text string, at time.Time) error {
		if at.IsZero() || at.After(time.Now().Add(time.Minute)) {
			at = time.Now().UTC()
		}
		if len(records) >= maxMessages || len(text) > 32768 {
			return ErrLimit
		}
		text = MaskSecrets(text)
		title = MaskSecrets(title)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		sum := sha256.Sum256([]byte(text))
		id := "chat-" + hex.EncodeToString(sum[:])
		records = append(records, Record{ID: id, Kind: "chat_excerpt", Source: source, Scope: "knowledge", Provenance: source + " sha256:" + digest, Timestamp: at.UTC(), Fields: map[string]Field{"title": {Value: title}, "role": {Value: role}, "content": {Value: text}}})
		return nil
	}
	if e := ParseChat(lr, format, appendText); e != nil {
		return nil, e
	}
	if lr.N <= 0 {
		return nil, ErrLimit
	}
	ids := make([]string, 0, len(records))
	e := s.change(func(m map[string]Record) error {
		ids = ids[:0]
		for _, r := range records {
			r = normalize(r)
			if e := valid(r); e != nil {
				return e
			}
			if _, exists := m[r.ID]; !exists {
				m[r.ID] = withRevision(r, Record{}, "import", "chat import")
			}
			ids = append(ids, r.ID)
		}
		return nil
	})
	return ids, e
}

// ParseChat streams the messages of a ChatGPT/Claude conversations.json or a
// Markdown file to emit (conversation title, role, text, time). Shared by the
// knowledge import and the memory distillation inbox (0.11.3 item 8).
func ParseChat(lr io.Reader, format string, emit func(title, role, text string, at time.Time) error) error {
	switch format {
	case "markdown":
		scanner := bufio.NewScanner(lr)
		scanner.Buffer(make([]byte, 4096), 32768)
		var chunk strings.Builder
		privateKey := false
		for scanner.Scan() {
			line := scanner.Text()
			if privateKey {
				if strings.Contains(line, "-----END") && strings.Contains(line, "PRIVATE KEY-----") {
					privateKey = false
				}
				continue
			}
			if strings.Contains(line, "-----BEGIN") && strings.Contains(line, "PRIVATE KEY-----") {
				line = "[REDACTED PRIVATE KEY]"
				privateKey = !strings.Contains(scanner.Text(), "-----END")
			}
			if chunk.Len()+len(line)+1 > 30000 {
				if e := emit("Markdown", "unknown", chunk.String(), time.Time{}); e != nil {
					return e
				}
				chunk.Reset()
			}
			chunk.WriteString(line)
			chunk.WriteByte('\n')
		}
		if e := scanner.Err(); e != nil {
			return ErrLimit
		}
		if e := emit("Markdown", "unknown", chunk.String(), time.Time{}); e != nil {
			return e
		}
	case "chatgpt", "claude":
		dec := json.NewDecoder(lr)
		token, e := dec.Token()
		if e != nil || token != json.Delim('[') {
			return ErrInvalid
		}
		for dec.More() {
			var c chatConversation
			if e = dec.Decode(&c); e != nil {
				return ErrInvalid
			}
			if format == "claude" {
				for _, m := range c.Messages {
					text := m.Text
					if text == "" {
						for _, part := range m.Content {
							if part.Type == "text" {
								text += part.Text
							}
						}
					}
					if e = emit(c.Name, m.Sender, text, chatTime(m.CreatedAt, 0, c.CreatedAt, c.CreateTime)); e != nil {
						return e
					}
				}
			} else {
				// IDs are sorted so identical exports are deterministic across map iteration.
				for _, id := range sortedChatKeys(c.Mapping) {
					m := c.Mapping[id].Message
					if m == nil {
						continue
					}
					var text strings.Builder
					for _, part := range m.Content.Parts {
						var p string
						if json.Unmarshal(part, &p) == nil {
							text.WriteString(p)
							text.WriteByte('\n')
						}
					}
					if e = emit(c.Title, m.Author.Role, text.String(), chatTime("", m.CreateTime, c.CreatedAt, c.CreateTime)); e != nil {
						return e
					}
				}
			}
		}
		if _, e = dec.Token(); e != nil {
			return ErrInvalid
		}
		if e = dec.Decode(new(any)); e != io.EOF {
			return ErrInvalid
		}
	default:
		return errors.New("chat format requires chatgpt/claude/markdown")
	}
	return nil
}

func sortedChatKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ImportChatZIP reads only the supported export member. No extraction to disk.
func (s *Store) ImportChatZIP(reader io.ReaderAt, size int64, format, source, digest string) ([]string, error) {
	if size < 1 || size > MaxChatBytes {
		return nil, ErrLimit
	}
	z, e := zip.NewReader(reader, size)
	if e != nil {
		return nil, ErrInvalid
	}
	if len(z.File) > 1000 {
		return nil, ErrLimit
	}
	var member *zip.File
	for _, f := range z.File {
		if f.Name == "conversations.json" {
			if member != nil || f.UncompressedSize64 > MaxChatBytes {
				return nil, ErrLimit
			}
			member = f
		}
	}
	if member == nil {
		return nil, ErrMissing
	}
	r, e := member.Open()
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return s.ImportChat(r, format, source, digest)
}
