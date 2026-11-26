package controlplane

// Bounded command/HTTP probes and small parsing helpers of the snapshot.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcpshka/internal/unitbus"
)

func dedupe(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func parseServiceStates(raw []byte) map[string]map[string]string {
	out := map[string]map[string]string{}
	current := map[string]string{}
	flush := func() {
		if id := current["Id"]; id != "" {
			copy := map[string]string{}
			for k, v := range current {
				copy[k] = v
			}
			out[id] = copy
		}
		current = map[string]string{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			current[k] = v
		}
	}
	flush()
	return out
}

func isActive(state map[string]string) bool {
	return state["LoadState"] == "loaded" && state["ActiveState"] == "active"
}

func stateWord(ready bool) string {
	if ready {
		return "ready"
	}
	return "down"
}

func compactReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

func executableRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

func localAddresses() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			host, _, err := net.ParseCIDR(addr.String())
			if err != nil || host.IsLoopback() || host.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, host.String())
			if len(out) >= 16 {
				return out, nil
			}
		}
	}
	return out, nil
}

func firstExisting(paths ...string) string {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

type cappedBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		_, _ = b.buf.Write(p[:keep])
	}
	if remaining <= 0 || len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func runBounded(ctx context.Context, path string, args ...string) ([]byte, bool, error) {
	if filepath.Base(path) == "systemctl" {
		// The control-path snapshot runs on every status read.
		if out, ok := unitbus.Show(ctx, args...); ok {
			return out, len(out) > maxOutputBytes, nil
		}
	}
	buf := &cappedBuffer{limit: maxOutputBytes}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	return append([]byte(nil), buf.buf.Bytes()...), buf.truncated, err
}

func httpProbe(ctx context.Context, url string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err.Error()
	}
	client := &http.Client{
		Timeout:       4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	detail := fmt.Sprintf("http=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	return resp.StatusCode >= 200 && resp.StatusCode < 300, detail
}

func bounded(data []byte) string {
	if len(data) > 2048 {
		data = data[:2048]
	}
	return strings.TrimSpace(string(data))
}
