package heavyqueue

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Production first: a heavy task waits (WAITING_RESOURCE, never FAIL) while
// memory or disk is low or a media stream is playing. A stream is measured by
// what the host sends, not by open sockets: go2rtc and the TV keep idle
// connections for hours.
var (
	MinMemAvailable int64 = 300 << 20
	MinDiskFree     int64 = 3 << 30
	MaxStreamTx     int64 = 1500 << 10 // bytes/s on the uplink

	memInfo    = "/proc/meminfo"
	diskPath   = "/var/lib"
	diskStatfs = syscall.Statfs
	txCounter  = "/sys/class/net/eth0/statistics/tx_bytes"

	txMu   sync.Mutex
	txLast struct {
		bytes int64
		at    time.Time
	}
)

func resourcesReady(now time.Time) (bool, string) {
	if ok, why := homeReady(now); !ok {
		return false, why
	}
	if mem := Memory(); mem.Low() {
		return false, "low memory: " + mem.String() + " (WAITING_RESOURCE)"
	}
	var st syscall.Statfs_t
	if diskStatfs(diskPath, &st) == nil {
		if free := int64(st.Bavail) * st.Bsize; free < MinDiskFree {
			return false, fmt.Sprintf("low disk: %d MiB free on %s (WAITING_RESOURCE)", free>>20, diskPath)
		}
	}
	if rate := txRate(now); rate > MaxStreamTx {
		return false, fmt.Sprintf("media stream active: uplink %d KiB/s (WAITING_RESOURCE)", rate>>10)
	}
	return true, ""
}

// HomeStatusFile is written each minute by the broker's homeguard; a fresh
// "degraded" holds new heavy work until Zigbee/MQTT recover (BUG-20261109-1).
// A missing, unreadable or stale file never blocks.
var HomeStatusFile = "/run/mcpshka-broker/home.json"

// HomeReady is the broker's last home probe (host_triage shows it).
func HomeReady() (bool, string) { return homeReady(time.Now()) }

func homeReady(now time.Time) (bool, string) {
	raw, err := os.ReadFile(HomeStatusFile)
	if err != nil {
		return true, ""
	}
	var st struct {
		State, Reason string
		At            time.Time
	}
	if json.Unmarshal(raw, &st) != nil || st.State != "degraded" || now.Sub(st.At) > 3*time.Minute {
		return true, ""
	}
	return false, "home degraded: " + st.Reason + " (WAITING_RESOURCE)"
}

// Mem is the host memory the governor decides on; -1 means unread.
type Mem struct{ Available, Total, SwapTotal, SwapFree int64 }

// PauseBelow and ResumeAbove bound a running heavy job's memory pause
// (internal/jobs): below the first it is frozen, above the second resumed.
var (
	PauseBelow  int64 = 250 << 20
	ResumeAbove int64 = 400 << 20
)

// SwapFull: on H96 zram is nearly full most of the day, so a full swap alone
// is normal; together with little RAM it means the next build thrashes.
func (m Mem) SwapFull() bool { return m.SwapTotal > 0 && m.SwapFree*10 < m.SwapTotal }

// Low holds new heavy work: below the floor, or near it with swap full.
func (m Mem) Low() bool {
	if m.Available < 0 {
		return false
	}
	return m.Available < MinMemAvailable || (m.SwapFull() && m.Available < 2*MinMemAvailable)
}

func (m Mem) String() string {
	s := fmt.Sprintf("%d MiB available", m.Available>>20)
	if m.SwapTotal > 0 {
		s += fmt.Sprintf(", swap %d%% used", 100-100*m.SwapFree/m.SwapTotal)
	}
	return s
}

// Memory reads /proc/meminfo (host_triage and the job pause use it too).
func Memory() Mem {
	m := Mem{Available: -1}
	raw, err := os.ReadFile(memInfo)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			m.Total = kb << 10
		case "MemAvailable:":
			m.Available = kb << 10
		case "SwapTotal:":
			m.SwapTotal = kb << 10
		case "SwapFree:":
			m.SwapFree = kb << 10
		}
	}
	return m
}

// txRate is the uplink rate since the previous poll (0 until two samples
// 1..60 s apart exist).
func txRate(now time.Time) int64 {
	raw, err := os.ReadFile(txCounter)
	if err != nil {
		return 0
	}
	cur, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	txMu.Lock()
	defer txMu.Unlock()
	prev, at := txLast.bytes, txLast.at
	txLast.bytes, txLast.at = cur, now
	gap := now.Sub(at)
	if at.IsZero() || gap < time.Second || gap > time.Minute || cur < prev {
		return 0
	}
	return (cur - prev) * int64(time.Second) / int64(gap)
}
