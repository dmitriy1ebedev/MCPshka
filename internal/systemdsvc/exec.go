package systemdsvc

// Bounded systemctl/journalctl runners and the unit allow-lists.

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/unitbus"
)

func addList(dst map[string]struct{}, raw string) {
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if validServiceName(item) {
			dst[item] = struct{}{}
		}
	}
}

func validServiceName(name string) bool {
	if len(name) < len("a.service") || len(name) > 128 || !strings.HasSuffix(name, ".service") || strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '@' || r == '_' || r == '-' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func cloneSet(src map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(src))
	for k := range src {
		out[k] = struct{}{}
	}
	return out
}

func runSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	if out, ok := unitbus.Show(ctx, args...); ok {
		return out, nil
	}
	cmd := exec.CommandContext(ctx, systemctlPath, args...)
	return cmd.CombinedOutput()
}

func runBrokerSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	return brokerclient.Run(ctx, "systemctl", args...)
}

type cappedBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		b.buf = append(b.buf, p[:keep]...)
	}
	if remaining <= 0 || len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func runJournalctl(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
	buf := &cappedBuffer{limit: maxBytes}
	cmd := exec.CommandContext(ctx, journalctlPath, args...)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	return append([]byte(nil), buf.buf...), buf.truncated, err
}

func runBrokerJournalctl(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
	raw, err := brokerclient.Run(ctx, "journalctl", args...)
	out, truncated := brokerclient.Cap(raw, maxBytes)
	return out, truncated, err
}

func bounded(data []byte) string {
	if len(data) > maxErrorBytes {
		data = data[:maxErrorBytes]
	}
	return strings.TrimSpace(string(data))
}

// StatusAllowed reports whether service_status and journal_read may read name.
func (s *Service) ownedUnits(ctx context.Context) map[string]string {
	if s.owned == nil {
		return nil
	}
	return s.owned(ctx)
}

// brokerOwnedUnits asks the broker, the authority that recorded them, which
// units executed approved plans own. Failure means none: status fails closed.
func brokerOwnedUnits(ctx context.Context) map[string]string {
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	raw, err := brokerclient.Run(child, "approved-task", "owned")
	if err != nil {
		return nil
	}
	var list []struct {
		Unit string `json:"unit"`
		Task string `json:"task"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make(map[string]string, len(list))
	for _, o := range list {
		if validServiceName(o.Unit) && len(o.Task) >= 16 {
			out[o.Unit] = o.Task[:16]
		}
	}
	return out
}

func (s *Service) StatusAllowed(name string) bool {
	_, ok := s.statusAllowed[name]
	return ok
}

// RestartAllowed reports whether a restart (service_action) may act on name.
func (s *Service) RestartAllowed(name string) bool {
	_, ok := s.restartAllowed[name]
	return ok
}
