package hostevents

import (
	"bufio"
	"mcpshka/internal/hostdiag"
	"regexp"
	"strings"
	"time"
)

// HostEvents are the root-only log facts host_diagnose needs: kernel
// messages and sshd lines that explain OOM kills, hung tasks, thread
// exhaustion and dropped SSH sessions. Fixed commands, fixed 24 h window,
// bounded lines; the controller cannot read the kernel ring or sshd's journal.
type HostEvents struct {
	WindowHours int             `json:"window_hours"`
	Kernel      []string        `json:"kernel"`
	SSH         []string        `json:"ssh"`
	Truncated   bool            `json:"truncated"`
	Errors      []string        `json:"errors,omitempty"`
	CapturedAt  time.Time       `json:"captured_at"`
	Counts      hostdiag.Counts `json:"counts"`
}

const hostEventsMaxLines = 40

var (
	kernelEvent = regexp.MustCompile(`(?i)out of memory|oom[-_ ]kill|killed process|hung_task|blocked for more than|segfault|fork.*(failed|retry)|cgroup: fork rejected|resource temporarily unavailable|i/o error|watchdog|link is down`)
	sshEvent    = regexp.MustCompile(`(?i)broken pipe|connection reset|timeout|timed out|fork|resource temporarily unavailable|cannot allocate|disconnected from|connection closed`)
)

func classifyHostLine(c *hostdiag.Counts, line string, ssh bool) {
	l := strings.ToLower(line)
	if ssh {
		if strings.Contains(l, "broken pipe") || strings.Contains(l, "connection reset") || strings.Contains(l, "timeout") || strings.Contains(l, "timed out") {
			c.SSHDrop++
		}
		if strings.Contains(l, "fork") || strings.Contains(l, "resource temporarily unavailable") {
			c.ForkFailed++
		}
		return
	}
	switch {
	case strings.Contains(l, "out of memory") || strings.Contains(l, "killed process") || strings.Contains(l, "oom-kill") || strings.Contains(l, "oom_kill"):
		c.OOMKill++
	case strings.Contains(l, "hung_task") || strings.Contains(l, "blocked for more than"):
		c.HungTask++
	case strings.Contains(l, "segfault"):
		c.Segfault++
	case strings.Contains(l, "fork") || strings.Contains(l, "resource temporarily unavailable"):
		c.ForkFailed++
	case strings.Contains(l, "i/o error"):
		c.IOError++
	}
}

// filterHostLines keeps the last max matching lines and counts every match.
func Filter(out string, ssh bool, c *hostdiag.Counts) ([]string, bool) {
	re := kernelEvent
	if ssh {
		re = sshEvent
	}
	var keep []string
	truncated := false
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		line := sc.Text()
		if !re.MatchString(line) {
			continue
		}
		classifyHostLine(c, line, ssh)
		if len(line) > 300 {
			line = line[:300]
		}
		keep = append(keep, line)
		if len(keep) > hostEventsMaxLines {
			keep, truncated = keep[1:], true
		}
	}
	return keep, truncated
}
