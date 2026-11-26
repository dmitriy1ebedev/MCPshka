// Package repoadopt hands a project directory to the MCPshka service user.
//
// /root/repos is root-owned, so the service can neither create a project
// there nor take over one that root left behind (a root git checkout, a
// project an AI created in /root). Under the owner's Face ID window the
// broker runs this package as root in a bounded unit: Preview reports and
// pins the exact change, Apply re-observes it and refuses any drift.
//
// The walk never follows a symlink. Every directory and regular file is
// opened with O_NOFOLLOW and changed through its descriptor, so a path the
// service swaps for a symlink between preview and apply is never chowned.
// Hard-linked files are accepted only while fs.protected_hardlinks is on (the
// service then cannot link a file it does not own; git clone of a local path
// hard-links its objects). Devices, sockets, FIFOs and other filesystems are
// rejected; one rejection holds the whole plan.
package repoadopt

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// Roots are variables only so tests can point them at a temporary tree.
var (
	ReposRoot = "/root/repos"
	homeRoot  = "/root"
)

const (
	// MaxEntries bounds one walk; larger trees are refused, not truncated.
	MaxEntries = 400000
	maxListed  = 20
)

var immediateName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Protected repositories stay root's: the protected integration store and
// the root ledger.
var protectedRepos = map[string]bool{"mcpshka-controlled": true, "rootledger": true}

// Directories directly under /root that are never moved into /root/repos.
var protectedSources = map[string]bool{"repos": true, "mcpshka-run": true, "go": true, "snap": true, "zigbee-backup": true}

// Plan is the pinned change: what will be moved, created and chowned.
type Plan struct {
	Repo          string   `json:"repo"`
	Source        string   `json:"source,omitempty"`
	Create        bool     `json:"create"`
	Entries       int      `json:"entries"`
	Change        int      `json:"change"`
	Rejected      []string `json:"rejected,omitempty"`
	RejectedCount int      `json:"rejected_count"`
	UID           int      `json:"uid"`
	GID           int      `json:"gid"`
	Digest        string   `json:"plan_sha256"`
	State         string   `json:"state"`
}

// ValidateTarget accepts an immediate, unprotected /root/repos/<name>.
func ValidateTarget(repo string) error {
	if filepath.Clean(repo) != repo || filepath.Dir(repo) != ReposRoot || !immediateName.MatchString(filepath.Base(repo)) {
		return errors.New("target must be an immediate /root/repos/<name>")
	}
	if protectedRepos[filepath.Base(repo)] {
		return errors.New("target is a protected root repository")
	}
	return nil
}

// ValidateSource accepts an immediate, non-hidden /root/<name> directory.
func ValidateSource(source string) error {
	if source == "" {
		return nil
	}
	name := filepath.Base(source)
	if filepath.Clean(source) != source || filepath.Dir(source) != homeRoot || !immediateName.MatchString(name) || protectedSources[name] {
		return errors.New("source must be an immediate, unprotected /root/<name> directory")
	}
	return nil
}

// Preview observes the change without making it.
func Preview(repo, source string, uid, gid int) (Plan, error) {
	if err := ValidateTarget(repo); err != nil {
		return Plan{}, err
	}
	if err := ValidateSource(source); err != nil {
		return Plan{}, err
	}
	p := Plan{Repo: repo, Source: source, UID: uid, GID: gid}
	h := sha256.New()
	fmt.Fprintf(h, "repoadopt/v1\x00%s\x00%s\x00%d\x00%d\x00", repo, source, uid, gid)
	root := repo
	_, err := os.Lstat(repo)
	switch {
	case source != "":
		if err == nil || !os.IsNotExist(err) {
			return Plan{}, errors.New("target already exists; move needs an absent target")
		}
		root = source
	case os.IsNotExist(err):
		p.Create = true
	case err != nil:
		return Plan{}, err
	}
	if p.Create {
		h.Write([]byte("create\x00"))
	} else if err := walk(root, uid, gid, &p, h); err != nil {
		return Plan{}, err
	}
	p.Digest = hex.EncodeToString(h.Sum(nil))
	p.State = "PREVIEW"
	if p.RejectedCount > 0 {
		p.State = "HOLD"
	}
	return p, nil
}

// Apply re-observes the plan, refuses drift or rejections, then performs it.
func Apply(repo, source string, uid, gid int, digest string) (Plan, error) {
	p, err := Preview(repo, source, uid, gid)
	if err != nil {
		return Plan{}, err
	}
	if p.Digest != digest {
		return p, errors.New("tree changed since preview: HOLD; preview again")
	}
	if p.RejectedCount > 0 {
		return p, fmt.Errorf("%d entries cannot be handed over safely: HOLD", p.RejectedCount)
	}
	if p.Create {
		if err := unix.Mkdir(repo, 0o770); err != nil {
			return p, err
		}
	} else if source != "" {
		// RENAME_NOREPLACE: never overwrite a target created after preview.
		if err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, repo, unix.RENAME_NOREPLACE); err != nil {
			return p, fmt.Errorf("move %s: %w", source, err)
		}
	}
	fd, err := unix.Open(repo, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return p, err
	}
	defer unix.Close(fd)
	if p.Create {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return p, err
		}
		// setgid keeps new files in the service group, like the other repos.
		if err := unix.Fchmod(fd, 0o2770); err != nil {
			return p, err
		}
	} else {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return p, err
		}
		if err := chownTree(fd, st.Dev, uid, gid); err != nil {
			return p, fmt.Errorf("handover incomplete, preview again: %w", err)
		}
	}
	p.State = "APPLIED"
	return p, nil
}

type entry struct {
	rel  string
	kind uint32
	uid  uint32
	gid  uint32
	ino  uint64
}

func walk(root string, uid, gid int, p *Plan, h interface{ Write([]byte) (int, error) }) error {
	var st unix.Stat_t
	if err := unix.Lstat(root, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("project root is not a directory")
	}
	dev := st.Dev
	links := HardlinksProtected()
	var changes []entry
	var visit func(abs, rel string, st unix.Stat_t) error
	visit = func(abs, rel string, st unix.Stat_t) error {
		p.Entries++
		if p.Entries > MaxEntries {
			return fmt.Errorf("more than %d entries: refused", MaxEntries)
		}
		kind := st.Mode & unix.S_IFMT
		reason := ""
		switch {
		case st.Dev != dev:
			reason = "other filesystem"
		case kind == unix.S_IFREG && st.Nlink > 1 && !links:
			reason = "hard link"
		case kind != unix.S_IFREG && kind != unix.S_IFDIR && kind != unix.S_IFLNK:
			reason = "special file"
		}
		if reason != "" {
			p.RejectedCount++
			if len(p.Rejected) < maxListed {
				p.Rejected = append(p.Rejected, rel+" ("+reason+")")
			}
			return nil
		}
		if int(st.Uid) != uid || int(st.Gid) != gid {
			p.Change++
			changes = append(changes, entry{rel, kind, st.Uid, st.Gid, st.Ino})
		}
		if kind != unix.S_IFDIR {
			return nil
		}
		names, err := readNames(abs)
		if err != nil {
			return err
		}
		for _, name := range names {
			var child unix.Stat_t
			if err := unix.Lstat(filepath.Join(abs, name), &child); err != nil {
				return err
			}
			if err := visit(filepath.Join(abs, name), filepath.Join(rel, name), child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, ".", st); err != nil {
		return err
	}
	for _, e := range changes {
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00%d\x00%d\n", e.rel, e.kind, e.uid, e.gid, e.ino)
	}
	fmt.Fprintf(h, "entries=%d rejected=%d\n", p.Entries, p.RejectedCount)
	return nil
}

// HardlinksProtected reports fs.protected_hardlinks=1.
func HardlinksProtected() bool {
	raw, err := os.ReadFile("/proc/sys/fs/protected_hardlinks")
	return err == nil && strings.TrimSpace(string(raw)) == "1"
}

func readNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	sort.Strings(names)
	return names, err
}

// chownTree changes dirfd's directory and everything under it through
// descriptors opened without following symlinks.
func chownTree(dirfd int, dev uint64, uid, gid int) error {
	var st unix.Stat_t
	if err := unix.Fstat(dirfd, &st); err != nil {
		return err
	}
	if st.Dev != dev {
		return errors.New("other filesystem")
	}
	if int(st.Uid) != uid || int(st.Gid) != gid {
		if err := unix.Fchown(dirfd, uid, gid); err != nil {
			return err
		}
	}
	dup, err := unix.Dup(dirfd)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(dup), "dir")
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.ContainsRune(name, '/') {
			return errors.New("invalid entry name")
		}
		var cst unix.Stat_t
		if err := unix.Fstatat(dirfd, name, &cst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch cst.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			if int(cst.Uid) != uid || int(cst.Gid) != gid {
				if err := unix.Fchownat(dirfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
					return err
				}
			}
		case unix.S_IFDIR:
			child, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			err = chownTree(child, dev, uid, gid)
			unix.Close(child)
			if err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := chownFile(dirfd, name, dev, uid, gid, HardlinksProtected()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: special file", name)
		}
	}
	return nil
}

func chownFile(dirfd int, name string, dev uint64, uid, gid int, links bool) error {
	// O_PATH cannot be fchowned; O_NONBLOCK keeps a FIFO swapped in from
	// blocking the open, and the fstat below rejects it.
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || (st.Nlink > 1 && !links) || st.Dev != dev {
		return fmt.Errorf("%s changed into a hard link or special file", name)
	}
	if int(st.Uid) == uid && int(st.Gid) == gid {
		return nil
	}
	return unix.Fchown(fd, uid, gid)
}
