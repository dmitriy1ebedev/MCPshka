package browser

// Small helpers of the browser runner: output tail, timeouts, navigation errors.

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func (s Spec) ProcessTimeout() time.Duration {
	if s.ProcessTimeoutSeconds == 0 {
		return 170 * time.Second
	}
	return time.Duration(s.ProcessTimeoutSeconds) * time.Second
}
func (s Spec) launchTimeout() time.Duration {
	if s.LaunchTimeoutMS == 0 {
		return 30 * time.Second
	}
	return time.Duration(s.LaunchTimeoutMS) * time.Millisecond
}
func navigationError(raw json.RawMessage) error {
	var r struct {
		ErrorText string `json:"errorText"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return errors.New("invalid navigation response")
	}
	if r.ErrorText != "" {
		return errors.New(r.ErrorText)
	}
	return nil
}
