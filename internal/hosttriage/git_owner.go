package hosttriage

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// reposDir is swapped by tests.
var reposDir = "/root/repos"

// gitOwnerProblems flags refs written by another user than the one who owns
// the repository's .git (MCP-254: git run as root left root-owned
// refs/remotes/* directories, and every later git gc by the service user
// failed with exit 128). Only refs and their logs are walked, at most 4096
// entries per repository, so the pass stays cheap.
func gitOwnerProblems(root string) []TriageProblem {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []TriageProblem
	for _, e := range entries {
		gitDir := filepath.Join(root, e.Name(), ".git")
		info, err := os.Lstat(gitDir)
		if err != nil || !info.IsDir() {
			continue
		}
		owner, ok := uidOf(info)
		if !ok {
			continue
		}
		foreign, first, seen := 0, "", 0
		for _, sub := range []string{"refs", "logs/refs"} {
			_ = filepath.WalkDir(filepath.Join(gitDir, sub), func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if seen++; seen > 4096 {
					return filepath.SkipAll
				}
				fi, err := d.Info()
				if err != nil {
					return nil
				}
				if uid, ok := uidOf(fi); ok && uid != owner {
					if foreign == 0 {
						first, _ = filepath.Rel(gitDir, path)
					}
					foreign++
				}
				return nil
			})
		}
		if foreign == 0 {
			continue
		}
		out = append(out, TriageProblem{Severity: "warning", Kind: "git-ownership", Subject: gitDir,
			Evidence: strconv.Itoa(foreign) + " refs not owned by the .git owner, first " + first,
			Action:   &TriageAction{Tool: "owner", Approval: "owner", Args: map[string]any{"run": "chown -R --reference=" + gitDir + " " + gitDir}},
			Note:     "git gc and ref updates by the service user fail until then; run git in this repository as its owner"})
	}
	return out
}

func uidOf(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}
