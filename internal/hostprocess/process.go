// Package hostprocess provides bounded read-only Linux process observations.
package hostprocess

import (
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/audit"
	"mcpshka/internal/systemdsvc"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Process struct {
	PID        int     `json:"pid"`
	PPID       int     `json:"ppid"`
	User       string  `json:"user"`
	CPU        float64 `json:"cpu_percent"`
	RSS        int64   `json:"rss_bytes"`
	Unit       string  `json:"unit"`
	Command    string  `json:"cmd"`
	StartTicks uint64  `json:"start_ticks"`
}

func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if len(b) > 65536 {
		return nil, errors.New("process observation exceeds limit")
	}
	return b, err
}
func Read(root string, pid int, uptime float64) (Process, error) {
	if pid <= 0 {
		return Process{}, errors.New("positive PID required")
	}
	dir := filepath.Join(root, strconv.Itoa(pid))
	raw, err := ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Process{}, err
	}
	p, err := ParseStat(string(raw), uptime)
	if err != nil {
		return p, err
	}
	status, err := ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return p, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			f := strings.Fields(line)
			if len(f) > 1 {
				p.User = f[1]
			}
		}
	}
	raw, _ = ReadFile(filepath.Join(dir, "cmdline"))
	cmd := strings.Join(audit.RedactArgs(strings.Split(string(raw), "\x00")), " ")
	cmd = systemdsvc.RedactLine(strings.TrimSpace(cmd))
	p.Command = string([]rune(cmd)[:min(len([]rune(cmd)), 256)])
	raw, err = ReadFile(filepath.Join(dir, "cgroup"))
	if err != nil {
		return p, err
	}
	for _, cg := range Cgroups(string(raw)) {
		for _, part := range strings.Split(cg, "/") {
			if strings.HasSuffix(part, ".service") || strings.HasSuffix(part, ".scope") {
				p.Unit = part
			}
		}
	}
	return p, nil
}
func ParseStat(raw string, uptime float64) (Process, error) {
	end := strings.LastIndex(raw, ")")
	start := strings.Index(raw, " (")
	if start < 1 || end < start {
		return Process{}, errors.New("invalid process stat")
	}
	pid, err := strconv.Atoi(raw[:start])
	if err != nil {
		return Process{}, err
	}
	f := strings.Fields(raw[end+1:])
	if len(f) < 22 {
		return Process{}, errors.New("short process stat")
	}
	values := make([]uint64, 0, 5)
	for _, i := range []int{1, 11, 12, 19, 21} {
		n, e := strconv.ParseUint(f[i], 10, 64)
		if e != nil {
			return Process{}, e
		}
		values = append(values, n)
	}
	elapsed := uptime - float64(values[3])/100
	cpu := float64(0)
	if elapsed > 0 {
		cpu = 100 * (float64(values[1]+values[2]) / 100) / elapsed
	}
	return Process{PID: pid, PPID: int(values[0]), CPU: cpu, RSS: int64(values[4]) * int64(os.Getpagesize()), StartTicks: values[3]}, nil
}
func Cgroups(raw string) []string {
	out := []string{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.SplitN(line, ":", 3)
		if len(f) == 3 && strings.HasPrefix(f[2], "/") {
			out = append(out, filepath.Clean(f[2]))
		}
	}
	return out
}
func List(filter, order string, limit int) ([]Process, error) {
	if len(filter) > 256 {
		return nil, errors.New("filter exceeds 256 bytes")
	}
	if order == "" {
		order = "cpu"
	}
	if order != "cpu" && order != "rss" && order != "pid" {
		return nil, errors.New("sort must be cpu, rss or pid")
	}
	raw, err := ReadFile("/proc/uptime")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return nil, errors.New("missing uptime")
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return nil, err
	}
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := []Process{}
	users := map[string]string{}
	for _, de := range dirs {
		pid, e := strconv.Atoi(de.Name())
		if e != nil {
			continue
		}
		p, e := Read("/proc", pid, uptime)
		if e != nil {
			continue
		}
		uid := p.User
		if name, ok := users[uid]; ok {
			p.User = name
		} else {
			if u, e := user.LookupId(uid); e == nil {
				p.User = u.Username
			}
			users[uid] = p.User
		}
		if filter != "" && !strings.Contains(strings.ToLower(fmt.Sprintf("%d %s %s %s", p.PID, p.User, p.Unit, p.Command)), strings.ToLower(filter)) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if order == "cpu" && out[i].CPU != out[j].CPU {
			return out[i].CPU > out[j].CPU
		}
		if order == "rss" && out[i].RSS != out[j].RSS {
			return out[i].RSS > out[j].RSS
		}
		return out[i].PID < out[j].PID
	})
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
