// Package hostdiag explains a host symptom as FACT → INFERENCE → ACTION.
// It only reads /proc, cgroups and the root broker's log facts; a symptom
// without a supporting fact stays UNKNOWN with the checks that would decide it.
package hostdiag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Fact struct {
	ID       string `json:"id"`
	State    string `json:"state"` // PASS, FAIL, UNKNOWN
	Value    string `json:"value"`
	Evidence string `json:"evidence,omitempty"`
}

type Inference struct {
	Claim   string   `json:"claim"`
	BasedOn []string `json:"based_on"`
}

type Result struct {
	Symptom       string      `json:"symptom"`
	Verdict       string      `json:"verdict"` // CAUSE_FOUND, NO_FAULT_FOUND, UNKNOWN
	Facts         []Fact      `json:"facts"`
	Inferences    []Inference `json:"inferences"`
	NextTools     []string    `json:"next_tools,omitempty"`
	MissingChecks []string    `json:"missing_checks,omitempty"`
	CheckedAt     time.Time   `json:"checked_at"`
}

// Events are the broker's kernel/sshd facts for the last 24 h.
type Events struct {
	Kernel []string `json:"kernel"`
	SSH    []string `json:"ssh"`
	Counts Counts   `json:"counts"`
}

type Counts struct {
	OOMKill    int `json:"oom_kill"`
	HungTask   int `json:"hung_task"`
	Segfault   int `json:"segfault"`
	ForkFailed int `json:"fork_failed"`
	IOError    int `json:"io_error"`
	SSHDrop    int `json:"ssh_drop"`
}

var Symptoms = map[string]bool{"general": true, "ssh_drop": true, "thread_eagain": true, "hang": true, "oom": true, "slow": true}

// Services whose pids/thread limits matter on H96.
var Services = []string{"mcpshka.service", "mcpshka-broker.service", "ssh.service", "docker.service", "containerd.service"}

// Roots are swapped by tests.
var Proc, Cgroup = "/proc", "/sys/fs/cgroup"

type run struct {
	r         Result
	fail      map[string]bool
	oomLog    bool
	forkUnits []string // units whose fork the pids controller rejected
}

var pidsReject = regexp.MustCompile(`fork rejected by pids controller in (\S+)`)

func (x *run) fact(id string, bad, unknown bool, value, evidence string) {
	state := "PASS"
	switch {
	case unknown:
		state = "UNKNOWN"
	case bad:
		state, x.fail[id] = "FAIL", true
	}
	x.r.Facts = append(x.r.Facts, Fact{id, state, value, evidence})
}

func (x *run) infer(claim string, ids ...string) {
	x.r.Inferences = append(x.r.Inferences, Inference{claim, ids})
}

func kv(path string) map[string]int64 {
	raw, _ := os.ReadFile(path)
	out := map[string]int64{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(strings.Replace(line, ":", " ", 1))
		if len(f) >= 2 {
			if v, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				out[f[0]] = v
			}
		}
	}
	return out
}

func num(path string) int64 {
	raw, _ := os.ReadFile(path)
	v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return -1 // missing or "max"
	}
	return v
}

// Diagnose gathers facts; events may be nil when the broker is unavailable.
// gap is the vmstat sampling interval: counters since boot say nothing about now.
func Diagnose(ctx context.Context, symptom string, events *Events, gap time.Duration) Result {
	x := &run{r: Result{Symptom: symptom, Facts: []Fact{}, Inferences: []Inference{}, CheckedAt: time.Now().UTC()}, fail: map[string]bool{}}

	cores := float64(runtime.NumCPU())
	if f := strings.Fields(readString(filepath.Join(Proc, "loadavg"))); len(f) >= 2 {
		l5, _ := strconv.ParseFloat(f[1], 64)
		x.fact("load", l5/cores > 1.5, false, fmt.Sprintf("load1 %s load5 %s on %.0f cores", f[0], f[1], cores), "")
	} else {
		x.fact("load", false, true, "", "loadavg unreadable")
	}

	mi := kv(filepath.Join(Proc, "meminfo"))
	if mi["MemTotal"] > 0 {
		avail := 100 * mi["MemAvailable"] / mi["MemTotal"]
		x.fact("memory", avail < 10, false, fmt.Sprintf("available %d%%, swap used %d MiB", avail, (mi["SwapTotal"]-mi["SwapFree"])/1024), "")
	} else {
		x.fact("memory", false, true, "", "meminfo unreadable")
	}

	a := kv(filepath.Join(Proc, "vmstat"))
	select {
	case <-ctx.Done():
	case <-time.After(gap):
	}
	b := kv(filepath.Join(Proc, "vmstat"))
	rate := func(keys ...string) float64 {
		var d int64
		for _, k := range keys {
			d += b[k] - a[k]
		}
		return float64(d) / gap.Seconds()
	}
	stall, swap, major := rate("allocstall_dma", "allocstall_dma32", "allocstall_normal", "allocstall_movable"), rate("pswpin", "pswpout"), rate("pgmajfault")
	x.fact("reclaim", stall > 50 || swap > 500 || major > 500, len(b) == 0, fmt.Sprintf("per s: allocstall %.0f, swap %.0f, major faults %.0f", stall, swap, major), "")
	x.fact("oom_counter", b["oom_kill"] > 0, len(b) == 0, fmt.Sprintf("%d OOM kills since boot", b["oom_kill"]), "")

	threads, top, topN := 0, "", 0
	entries, _ := os.ReadDir(Proc)
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		st := kv(filepath.Join(Proc, e.Name(), "status"))
		n := int(st["Threads"])
		threads += n
		if n > topN {
			topN, top = n, strings.TrimSpace(strings.TrimPrefix(firstLine(filepath.Join(Proc, e.Name(), "status")), "Name:"))
		}
	}
	tmax := num(filepath.Join(Proc, "sys/kernel/threads-max"))
	x.fact("threads", tmax > 0 && int64(threads) > tmax*8/10, threads == 0, fmt.Sprintf("%d threads of threads-max %d; most: %s %d", threads, tmax, top, topN), "")

	for _, u := range Services {
		dir := filepath.Join(Cgroup, "system.slice", u)
		cur, max := num(filepath.Join(dir, "pids.current")), num(filepath.Join(dir, "pids.max"))
		if cur < 0 {
			continue // service not present on this host
		}
		nproc := "?"
		if pids := strings.Fields(readString(filepath.Join(dir, "cgroup.procs"))); len(pids) > 0 {
			nproc = nprocLimit(pids[0])
		}
		x.fact("tasks:"+u, max > 0 && cur >= max*9/10, false, fmt.Sprintf("tasks %d/%s, RLIMIT_NPROC %s", cur, limitString(max), nproc), "")
	}

	if events == nil {
		x.fact("kernel_log", false, true, "", "broker host_events unavailable")
		x.fact("ssh_log", false, true, "", "broker host_events unavailable")
	} else {
		c := events.Counts
		x.oomLog = c.OOMKill > 0
		seen := map[string]bool{}
		for _, line := range events.Kernel {
			if m := pidsReject.FindStringSubmatch(line); m != nil && !seen[m[1]] {
				seen[m[1]] = true
				x.forkUnits = append(x.forkUnits, filepath.Base(m[1]))
			}
		}
		x.fact("kernel_log", c.OOMKill+c.HungTask+c.ForkFailed+c.IOError > 0, false, fmt.Sprintf("24h: oom %d, hung_task %d, segfault %d, fork failures %d, I/O errors %d", c.OOMKill, c.HungTask, c.Segfault, c.ForkFailed, c.IOError), strings.Join(lastN(events.Kernel, 4), " | "))
		x.fact("ssh_log", c.SSHDrop > 0, false, fmt.Sprintf("24h: %d drop/timeout lines", c.SSHDrop), strings.Join(lastN(events.SSH, 4), " | "))
	}
	x.reason()
	return x.r
}

func (x *run) reason() {
	f := x.fail
	if f["oom_counter"] || x.oomLog {
		x.infer("the kernel OOM killer ran", "oom_counter", "kernel_log")
	}
	pressure := []string{}
	for _, id := range []string{"memory", "reclaim", "load"} {
		if f[id] {
			pressure = append(pressure, id)
		}
	}
	if len(pressure) > 0 {
		x.infer("host under pressure ("+strings.Join(pressure, ", ")+"): every process stalls, which looks like a hang", pressure...)
		x.r.NextTools = append(x.r.NextTools, "docker_resources", "host_triage")
	}
	var tasks []string
	for id := range f {
		if strings.HasPrefix(id, "tasks:") || id == "threads" {
			tasks = append(tasks, id)
		}
	}
	if len(tasks) > 0 {
		x.infer("task/pid limit nearly reached: new threads fail with EAGAIN", tasks...)
		x.r.NextTools = append(x.r.NextTools, "service_status")
	}
	if len(x.forkUnits) > 0 {
		x.infer("the pids controller rejected fork in "+strings.Join(x.forkUnits, ", ")+" during the last 24 h: its TasksMax is too low for peaks (operations then fail with EAGAIN or time out)", "kernel_log")
	}
	if f["ssh_log"] {
		if len(pressure) > 0 {
			x.infer("sshd dropped sessions while the host was under pressure: the stall, not the network, is the likely cause", append([]string{"ssh_log"}, pressure...)...)
		} else {
			x.infer("sshd dropped sessions without host pressure now: check the network path", "ssh_log")
			x.r.NextTools = append(x.r.NextTools, "network_connectivity")
		}
	}
	switch {
	case len(x.r.Inferences) > 0:
		x.r.Verdict = "CAUSE_FOUND"
		return
	case hasUnknown(x.r):
		x.r.Verdict = "UNKNOWN"
	default:
		x.r.Verdict = "NO_FAULT_FOUND"
	}
	if m, ok := missing[x.r.Symptom]; ok {
		x.r.MissingChecks = m
	}
}

var missing = map[string][]string{
	"ssh_drop":      {"repeat host_diagnose right after the next drop (vmstat is a 2 s sample)", "network_connectivity during the drop", "client ssh -vvv log"},
	"thread_eagain": {"the failing process's /proc/<pid>/limits and its cgroup pids.max at failure time"},
	"hang":          {"repeat during the hang"},
	"slow":          {"repeat during the slowdown"},
	"oom":           {"memory.events of the suspected container (docker_resources)"},
}

func readString(p string) string { b, _ := os.ReadFile(p); return string(b) }
func firstLine(p string) string  { l, _, _ := strings.Cut(readString(p), "\n"); return l }

func nprocLimit(pid string) string {
	for _, line := range strings.Split(readString(filepath.Join(Proc, pid, "limits")), "\n") {
		if f := strings.Fields(strings.TrimPrefix(line, "Max processes")); strings.HasPrefix(line, "Max processes") && len(f) > 0 {
			return f[0]
		}
	}
	return "?"
}

func limitString(v int64) string {
	if v < 0 {
		return "max"
	}
	return strconv.FormatInt(v, 10)
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func hasUnknown(r Result) bool {
	for _, f := range r.Facts {
		if f.State == "UNKNOWN" {
			return true
		}
	}
	return false
}
