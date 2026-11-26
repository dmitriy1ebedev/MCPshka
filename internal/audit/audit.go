package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Time     time.Time      "json:\"time\""
	Type     string         "json:\"type\""
	JobID    string         "json:\"job_id,omitempty\""
	Command  string         "json:\"command,omitempty\""
	Args     []string       "json:\"args,omitempty\""
	Workdir  string         "json:\"workdir,omitempty\""
	Result   string         "json:\"result,omitempty\""
	Duration int64          "json:\"duration_ms,omitempty\""
	Details  map[string]any "json:\"details,omitempty\""
}

type Logger struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
}

func New(path string, maxBytes int64) (*Logger, error) {
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return &Logger{path: path, maxBytes: maxBytes}, nil
}

func (l *Logger) Log(e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Args = RedactArgs(e.Args)

	if err := l.rotateIfNeeded(); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(e)
}

func (l *Logger) rotateIfNeeded() error {
	info, err := os.Stat(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < l.maxBytes {
		return nil
	}
	base := time.Now().UTC().UnixNano()
	for i := int64(0); i < 100; i++ {
		rotated := fmt.Sprintf("%s.%d", l.path, base+i)
		if _, err := os.Lstat(rotated); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		return os.Rename(l.path, rotated)
	}
	return fmt.Errorf("unable to allocate append-only audit segment")
}

func RedactArgs(args []string) []string {
	out := append([]string(nil), args...)
	redactNext := false
	for i, arg := range out {
		lower := strings.ToLower(arg)
		if redactNext {
			out[i] = "[REDACTED]"
			redactNext = false
			continue
		}
		if isSecretFlag(lower) {
			redactNext = true
			continue
		}
		if k, _, ok := strings.Cut(arg, "="); ok && isSecretName(strings.ToLower(strings.TrimLeft(k, "-"))) {
			out[i] = k + "=[REDACTED]"
		}
	}
	return out
}

func isSecretFlag(s string) bool {
	s = strings.TrimLeft(s, "-")
	return isSecretName(s)
}

func isSecretName(s string) bool {
	for _, needle := range []string{"password", "passwd", "token", "secret", "api_key", "apikey", "private_key"} {
		if s == needle || strings.HasSuffix(s, "_"+needle) {
			return true
		}
	}
	return false
}
