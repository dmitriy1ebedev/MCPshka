package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// HEAD and its tree change only when HEAD, the branch ref or packed-refs
// change, so their identity is cached by the stat signature of those files.
// Idle missions and health probes asked git ~120 times a minute (MCP-164).
var identityCache = struct {
	sync.Mutex
	m map[string][2]string // root+object -> {signature, value}
}{m: map[string][2]string{}}

func statSig(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return path + ":-"
	}
	return fmt.Sprintf("%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
}

// refSignature returns "" when the layout is unknown (no caching then).
func refSignature(root string) string {
	gitdir := filepath.Join(root, ".git")
	if info, err := os.Lstat(gitdir); err != nil {
		return ""
	} else if !info.IsDir() {
		raw, err := os.ReadFile(gitdir) // worktree: "gitdir: <path>"
		if err != nil {
			return ""
		}
		dir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
		if !ok {
			return ""
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		gitdir = dir
	}
	common := gitdir
	if raw, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		c := strings.TrimSpace(string(raw))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		common = c
	}
	head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return ""
	}
	sig := []string{string(head), statSig(filepath.Join(common, "packed-refs"))}
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: "); ok {
		if strings.Contains(ref, "..") {
			return ""
		}
		sig = append(sig, statSig(filepath.Join(gitdir, ref)), statSig(filepath.Join(common, ref)))
	}
	return strings.Join(sig, "|")
}

func cachedIdentity(root, object string, read func() (string, error)) (string, error) {
	if object != "HEAD" && object != "HEAD^{tree}" {
		return read()
	}
	sig := refSignature(root)
	key := root + "\x00" + object
	if sig != "" {
		identityCache.Lock()
		hit, ok := identityCache.m[key]
		identityCache.Unlock()
		if ok && hit[0] == sig {
			return hit[1], nil
		}
	}
	value, err := read()
	if err == nil && sig != "" && sig == refSignature(root) {
		identityCache.Lock()
		if len(identityCache.m) > 256 {
			identityCache.m = map[string][2]string{}
		}
		identityCache.m[key] = [2]string{sig, value}
		identityCache.Unlock()
	}
	return value, err
}

// HeadIdentity is the cached HEAD or HEAD^{tree} of a repository root for
// callers outside this policy (acceptance evidence).
func HeadIdentity(root, object string, read func() (string, error)) (string, error) {
	return cachedIdentity(filepath.Clean(root), object, read)
}
