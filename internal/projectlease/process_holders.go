package projectlease

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ProcessHolder is one live process that references a sealed recovery root.
// It is operator guidance only; it never authorizes signalling the process.
type ProcessHolder struct {
	PID            int    `json:"pid"`
	UID            int    `json:"uid"`
	Command        string `json:"command"`
	Reference      string `json:"reference"`
	Path           string `json:"path"`
	Unit           string `json:"unit,omitempty"`
	Recommendation string `json:"recommendation"`
}

const maxReportedHolders = 16

// sealedProcessHolders inspects every process. Inspection failures are errors;
// references are collected (bounded) so the operator sees all blockers at once.
func sealedProcessHolders(roots []string) ([]ProcessHolder, error) {
	return processHolders(roots, true)
}

// Unsealed stale worktrees must include unprivileged CWDs and deleted handles.
// A read-only regular-file descriptor cannot change the worktree (leaked by
// long-lived tool servers such as Desktop Commander), so it never makes a
// stale lease look alive; directories, writable files, CWDs and maps still do.
func processHolders(roots []string, sealed bool) ([]ProcessHolder, error) {
	pids, e := os.ReadDir("/proc")
	if e != nil {
		return nil, e
	}
	match := func(s string) bool {
		for _, r := range roots {
			if s == r || strings.HasPrefix(s, r+"/") {
				return true
			}
		}
		return false
	}
	var holders []ProcessHolder
	add := func(prefix, pid, ref, path string) {
		if len(holders) < maxReportedHolders {
			holders = append(holders, describeHolder(prefix, pid, ref, path))
		}
	}
	for _, p := range pids {
		if _, e := strconv.Atoi(p.Name()); e != nil {
			continue
		}
		prefix := "/proc/" + p.Name()
		fds, e := os.ReadDir(prefix + "/fd")
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return holders, errors.New("process descriptor inspection unavailable")
		}
		if len(fds) > 65536 {
			return holders, errors.New("descriptor inspection budget exceeded")
		}
		entries := []string{prefix + "/cwd", prefix + "/root"}
		for _, fd := range fds {
			entries = append(entries, prefix+"/fd/"+fd.Name())
		}
		found := false
		for _, entry := range entries {
			dest, e := os.Readlink(entry)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return holders, errors.New("process reference inspection unavailable")
			}
			if match(dest) && !(sealed && entry == prefix+"/cwd" && unprivilegedSealedCWD(prefix)) && !(sealed && detachedReadOnlyFD(entry, dest)) && !(!sealed && readOnlyFileFD(entry)) {
				add(prefix, p.Name(), strings.TrimPrefix(entry, prefix+"/"), dest)
				found = true
				break
			}
		}
		if found {
			continue
		}
		maps, e := os.ReadFile(prefix + "/maps")
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return holders, errors.New("mapped-file inspection unavailable")
		}
		if len(maps) > 4<<20 {
			return holders, errors.New("mapped-file inspection budget exceeded")
		}
		for _, line := range strings.Split(string(maps), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 6 && match(strings.Join(fields[5:], " ")) {
				add(prefix, p.Name(), "maps", strings.Join(fields[5:], " "))
				break
			}
		}
	}
	return holders, nil
}

func noSealedProcessReferences(roots []string) error {
	holders, err := sealedProcessHolders(roots)
	if err != nil {
		return err
	}
	if len(holders) > 0 {
		return fmt.Errorf("live process references: %s", summarizeHolders(holders))
	}
	return nil
}

func summarizeHolders(holders []ProcessHolder) string {
	parts := make([]string, 0, len(holders))
	for _, h := range holders {
		parts = append(parts, fmt.Sprintf("pid %d uid %d %q %s=%s (%s)", h.PID, h.UID, h.Command, h.Reference, h.Path, h.Recommendation))
	}
	return strings.Join(parts, "; ")
}

func describeHolder(prefix, pid, ref, path string) ProcessHolder {
	h := ProcessHolder{UID: -1, Reference: ref, Path: path}
	h.PID, _ = strconv.Atoi(pid)
	if b, e := os.ReadFile(prefix + "/status"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "Uid:" {
				h.UID, _ = strconv.Atoi(f[1])
			}
		}
	}
	if b, e := os.ReadFile(prefix + "/cmdline"); e == nil {
		h.Command = strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
	}
	if b, e := os.ReadFile(prefix + "/cgroup"); e == nil {
		h.Unit = systemUnit(string(b))
	}
	if len(h.Command) > 120 {
		h.Command = h.Command[:120]
	}
	h.Recommendation = holderRecommendation(h)
	return h
}

func holderRecommendation(h ProcessHolder) string {
	switch {
	case strings.Contains(h.Command, "bg-spare"):
		return "idle Claude Code spare worker inherited this directory; stop it and launch Claude Code from outside the repository"
	case strings.Contains(h.Command, "claude"):
		return "Claude Code session works inside the repository; finish or move it to a sibling worktree"
	case h.Reference == "cwd":
		return "process working directory is inside the repository; stop it or change its directory"
	case h.Reference == "maps":
		return "process executes or maps a repository file; stop it"
	case h.Unit != "":
		return "service " + h.Unit + " holds a repository file open for writing or as a directory; restart it (service_action restart " + h.Unit + ")"
	default:
		return "process holds an open repository file; close it or stop the process"
	}
}

// readOnlyFileFD reports a descriptor of a regular file opened O_RDONLY.
func readOnlyFileFD(entry string) bool {
	if !strings.Contains(entry, "/fd/") {
		return false
	}
	if info, e := os.Stat(entry); e != nil || !info.Mode().IsRegular() {
		return false
	}
	b, e := os.ReadFile(strings.Replace(entry, "/fd/", "/fdinfo/", 1))
	if e != nil || len(b) > 8192 {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "flags:" {
			flags, e := strconv.ParseUint(f[1], 8, 64)
			return e == nil && flags&uint64(syscall.O_ACCMODE) == uint64(syscall.O_RDONLY)
		}
	}
	return false
}

// systemUnit names the system service of a cgroup v2 path, or "" for
// sessions, scopes and anything outside system.slice.
func systemUnit(cgroup string) string {
	for _, line := range strings.Split(cgroup, "\n") {
		if rest, ok := strings.CutPrefix(line, "0::/system.slice/"); ok && strings.HasSuffix(rest, ".service") && !strings.Contains(rest, "/") {
			return rest
		}
	}
	return ""
}
