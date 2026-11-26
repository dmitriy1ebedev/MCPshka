package wifiscan

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/boundedbuf"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A Wi-Fi scan needs CAP_NET_ADMIN, which DIAG must never get: the right also
// rewrites routes and links. Root only refreshes the kernel's BSS cache with
// one fixed command; DIAG then reads it unprivileged (`wifi --cache`). No
// association, link, rfkill or address change is possible through this path.
var (
	wifiInterface = regexp.MustCompile(`^wl[a-z0-9]{1,13}$`)
	wifiScanMu    sync.Mutex
	wifiScanLast  time.Time
)

const wifiScanInterval = 15 * time.Second

func validateWifiScan(args []string) error {
	if len(args) != 1 || !wifiInterface.MatchString(args[0]) {
		return errors.New("wifi scan requires one wireless interface name")
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", args[0], "wireless")); err != nil {
		return errors.New("wifi scan: interface is not wireless")
	}
	return nil
}

func Run(ctx context.Context, args []string) ([]byte, error) {
	if err := validateWifiScan(args); err != nil {
		return nil, err
	}
	wifiScanMu.Lock()
	defer wifiScanMu.Unlock()
	if wait := wifiScanInterval - time.Since(wifiScanLast); wait > 0 {
		return nil, errors.New("wifi scan rate limited: retry in " + wait.Round(time.Second).String())
	}
	wifiScanLast = time.Now()
	child, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "/usr/sbin/iw", "dev", args[0], "scan")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	var out boundedbuf.Buffer
	out.Limit = maxOutputBytes
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	text := string(out.Bytes())
	if runErr != nil {
		reason := strings.TrimSpace(text)
		if len(reason) > 200 {
			reason = reason[:200]
		}
		return nil, errors.New("wifi scan failed: " + reason)
	}
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "BSS ") {
			count++
		}
	}
	return json.Marshal(map[string]any{"interface": args[0], "bss_seen": count, "scanned_at": time.Now().UTC(), "cache_valid_seconds": 30})
}
