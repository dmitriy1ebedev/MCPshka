package homeguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"mcpshka/internal/heavyqueue"
)

const UnitRoot = "/etc/systemd/system"
const BackupFile = "/var/lib/mcpshka-executor/home-priority/backup.json"
const homeDropIn = "50-gateway-priority.conf"

// rolledBackMark is what a release rollback writes into OffFile.
const rolledBackMark = "home priority rolled back\n"

const serverDelegate = "mcpshka.service.d/50-mcpshka-delegate.conf"
const homeText = "[Service]\nCPUWeight=1000\nIOWeight=1000\n"
const sliceText = "[Unit]\nDescription=MCPshka heavy work\n[Slice]\nCPUWeight=20\nIOWeight=10\nCPUQuota=300%\n"

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9_.@\\:-]+\.service$`)

// HomeUnits uses loaded host services, including inactive ones; removed units disappear.
func HomeUnits(ctx context.Context, run Exec) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := run(ctx, "/usr/bin/systemctl", "list-units", "--all", "--type=service", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		return nil, fmt.Errorf("discover home units: %w", err)
	}
	found := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		u := f[0]
		if f[1] == "masked" || f[1] == "not-found" {
			log.Printf("home priority: skip %s (%s)", u, f[1])
			continue
		}
		if !serviceName.MatchString(u) || strings.Contains(u, "..") {
			continue
		}
		if u == "docker.service" || u == "containerd.service" || u == "dnsmasq.service" || u == "tailscaled.service" || u == "laned.service" || strings.HasPrefix(u, "lane-") {
			found[u] = true
		}
	}
	names := make([]string, 0, len(found))
	for u := range found {
		names = append(names, u)
	}
	sort.Strings(names)
	return names, nil
}

// Recheck before touching a stale service backup; removed entries can still
// restore their original files, but masked/not-found units keep the owner's policy.
func unavailableUnit(ctx context.Context, run Exec, unit string) bool {
	raw, err := run(ctx, "/usr/bin/systemctl", "show", "--property=LoadState", "--value", "--", unit)
	state := strings.TrimSpace(string(raw))
	return err != nil || state == "masked" || state == "not-found"
}

type savedFile struct {
	Before []byte
	Mode   os.FileMode
	Exists bool
	After  string
}
type backup struct {
	Files   map[string]savedFile
	Runtime map[string][]string
}

func desiredFiles(names []string) map[string]string {
	out := map[string]string{
		heavyqueue.HeavySlice:                   sliceText,
		"user.slice.d/50-mcpshka-priority.conf": "[Slice]\nCPUWeight=30\nIOWeight=30\nCPUQuota=300%\n",
		// P5.3: the server splits its own cgroup into server/ and heavy/ (jobs/cgroup.go).
		serverDelegate: "[Service]\nDelegate=yes\nProtectControlGroups=no\n",
	}
	for _, u := range names {
		out[u+".d/"+homeDropIn] = homeText
	}
	return out
}

// safePath rejects symlinks in every existing component before root-owned writes.
func safePath(path string) error {
	path = filepath.Clean(path)
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %s: HOLD", p)
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}
func replaceFile(path string, data []byte, mode os.FileMode) error {
	if err := safePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".home-priority-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func readSaved(path string) (savedFile, error) {
	if err := safePath(path); err != nil {
		return savedFile{}, err
	}
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return savedFile{}, nil
	}
	if err != nil {
		return savedFile{}, err
	}
	if !st.Mode().IsRegular() || st.Size() > 65536 {
		return savedFile{}, fmt.Errorf("unsafe priority file %s: HOLD", path)
	}
	raw, err := os.ReadFile(path)
	return savedFile{Before: raw, Mode: st.Mode().Perm(), Exists: true}, err
}
func restoreFile(path string, s savedFile) error {
	cur, err := readSaved(path)
	if err != nil {
		return err
	}
	// A retry may see either the managed content or the already restored original.
	if cur.Exists == s.Exists && bytes.Equal(cur.Before, s.Before) && (!s.Exists || cur.Mode == s.Mode) {
		return nil
	}
	if !cur.Exists || (string(cur.Before) != s.After && !sameDirectives(string(cur.Before), s.After)) {
		return fmt.Errorf("foreign priority change %s: HOLD", path)
	}
	if s.Exists {
		return replaceFile(path, s.Before, s.Mode)
	}
	return os.Remove(path)
}

// sameDirectives compares unit files without comments and blank lines.
func sameDirectives(a, b string) bool {
	strip := func(s string) string {
		var out []string
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, ";") {
				out = append(out, line)
			}
		}
		return strings.Join(out, "\n")
	}
	return strip(a) == strip(b)
}

// Install journals originals before any mutation. It is restartable after partial writes
// or daemon-reload failure; rollback refuses to overwrite a later owner's change.
func Install(ctx context.Context, run Exec, root, state string, rollback bool) error {
	return withBackup(state, func(b *backup, exists bool) error {
		if rollback && !exists {
			return nil
		}
		current, err := runtimeUnits(ctx, run)
		if err != nil {
			return err
		}
		if !rollback {
			if err := rememberRuntime(ctx, run, b, current); err != nil {
				return err
			}
		}
		want := map[string]string{}
		if !rollback {
			var names []string
			for u := range current {
				if strings.HasSuffix(u, ".service") {
					names = append(names, u)
				}
			}
			want = desiredFiles(names)
		}
		skippedPolicy := false
		for rel, s := range b.Files {
			if !validPriorityPath(rel) {
				return errors.New("invalid priority backup path: HOLD")
			}
			if u, tail, ok := strings.Cut(rel, ".d/"); ok && tail == homeDropIn {
				if _, available := current[u]; !available && unavailableUnit(ctx, run, u) {
					log.Printf("home priority: skip unavailable %s policy; keep backup", u)
					skippedPolicy = true
					continue
				}
			}
			if _, keep := want[rel]; !keep {
				if err := restoreFile(filepath.Join(root, rel), s); err != nil {
					return err
				}
			}
		}
		for rel, content := range want {
			if _, ok := b.Files[rel]; !ok {
				s, err := readSaved(filepath.Join(root, rel))
				if err != nil {
					return err
				}
				s.After = content
				b.Files[rel] = s
			}
		}
		if !rollback || exists {
			data, err := json.Marshal(b)
			if err != nil {
				return err
			}
			if err = replaceFile(state, data, 0600); err != nil {
				return err
			}
		}
		for rel, content := range want {
			path := filepath.Join(root, rel)
			s := b.Files[rel]
			cur, err := readSaved(path)
			if err != nil {
				return err
			}
			if cur.Exists && string(cur.Before) == content {
				continue
			}
			// A hand-written copy with the same directives (comments aside) is
			// ours in effect: adopt it instead of holding the whole policy.
			adopt := cur.Exists && sameDirectives(string(cur.Before), content)
			if !adopt && (cur.Exists != s.Exists || !bytes.Equal(cur.Before, s.Before)) {
				return fmt.Errorf("foreign priority change %s: HOLD", path)
			}
			if err = replaceFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}
		if !exists && rollback {
			return nil
		}
		if out, err := run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("home priority daemon-reload: %w: %.256s", err, out)
		}
		if rollback {
			if err := restoreRuntime(ctx, run, b, current); err != nil {
				return err
			}
			if skippedPolicy {
				return nil
			}
			return os.Remove(state)
		}
		return nil
	})
}

func validPriorityPath(rel string) bool {
	if rel == heavyqueue.HeavySlice || rel == "user.slice.d/50-mcpshka-priority.conf" || rel == serverDelegate {
		return true
	}
	u, tail, ok := strings.Cut(rel, ".d/")
	return ok && tail == homeDropIn && serviceName.MatchString(u) && !strings.Contains(u, "..") && (u == "docker.service" || u == "containerd.service" || u == "dnsmasq.service" || u == "tailscaled.service" || u == "laned.service" || strings.HasPrefix(u, "lane-"))
}

// Command is the existing root broker's local apply/rollback entry point.
func Command(ctx context.Context, args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("home priority requires root")
	}
	startup := len(args) == 1 && args[0] == "--startup"
	rollback := len(args) == 1 && args[0] == "--rollback"
	if len(args) != 0 && !rollback && !startup {
		return errors.New("usage: home-priority [--rollback]")
	}
	if startup {
		if keep, err := keepOff(OffFile); err != nil || keep {
			return err
		}
	}
	if rollback {
		if err := replaceFile(OffFile, []byte(rolledBackMark), 0600); err != nil {
			return err
		}
	} else if err := os.Remove(OffFile); !startup && err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := Install(ctx, hostExec, UnitRoot, BackupFile, rollback); err != nil {
		return err
	}
	if rollback {
		return nil
	}
	return Weights(ctx, hostExec)
}

func withBackup(state string, fn func(*backup, bool) error) error {
	if err := safePath(state); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		return err
	}
	lockPath := state + ".lock"
	if err := safePath(lockPath); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("home priority busy: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	b := backup{Files: map[string]savedFile{}}
	raw, err := os.ReadFile(state)
	if err == nil {
		if len(raw) > 1<<20 {
			return errors.New("priority backup exceeds budget: HOLD")
		}
		if err = json.Unmarshal(raw, &b); err != nil || b.Files == nil {
			return errors.New("invalid priority backup: HOLD")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	return fn(&b, err == nil)
}

// keepOff decides a --startup run: the owner's off switch is kept, while the
// mark a failed upgrade's rollback left is cleared so the next good install
// turns priority back on (23.11: it stayed off for a day after a rollback).
func keepOff(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if string(raw) != rolledBackMark {
		return true, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}
