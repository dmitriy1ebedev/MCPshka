package gitops

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The private hook reads the temporary index prepared by git commit --only.
// Git publishes the commit only if the selected index entries pass this check.
const commitIndexHook = `#!/bin/bash
set -euo pipefail
export LC_ALL=C

conflict() {
	printf 'CONCURRENT_WRITER_CONFLICT: staged entry differs for %q\n' "$1" >&2
	exit 1
}

verify() {
	local row mode object stage size digest
	local -a rows=()
	mapfile -d '' -n 3 -t rows < <(git --literal-pathspecs ls-files --stage -z -- "$1" && printf '\0')
	if [[ "$2" == DELETED ]]; then
		[[ ${#rows[@]} == 1 && -z "${rows[0]}" ]] || conflict "$1"
		return
	fi
	[[ ${#rows[@]} == 2 && -z "${rows[1]}" ]] || conflict "$1"
	row=${rows[0]}
	[[ "${row#*$'\t'}" == "$1" ]] || conflict "$1"
	read -r mode object stage <<< "${row%%$'\t'*}"
	[[ "$stage" == 0 && ( "$mode" == 100644 || "$mode" == 100755 ) ]] || conflict "$1"
	size=$(git cat-file -s "$object") || conflict "$1"
	[[ "$size" -le 16777216 ]] || conflict "$1"
	digest=$(git cat-file blob "$object" | sha256sum) || conflict "$1"
	[[ "${digest%% *}" == "$2" ]] || conflict "$1"
}

[[ -n "${GIT_INDEX_FILE:-}" ]] || conflict 'Git index unavailable'
`

func verifiedCommitHooks(files []string, expected map[string]string) (string, error) {
	if len(expected) != len(files) {
		return "", fmt.Errorf("expected_files must cover every commit path exactly")
	}
	var hook strings.Builder
	hook.WriteString(commitIndexHook)
	seen := make(map[string]bool, len(files))
	for _, path := range files {
		hash, present := expected[path]
		_, decodeErr := hex.DecodeString(hash)
		if seen[path] || !present || (hash != "DELETED" && (len(hash) != 64 || decodeErr != nil)) {
			return "", fmt.Errorf("invalid or duplicate expected commit entry %q", path)
		}
		seen[path] = true
		if hash != "DELETED" {
			hash = strings.ToLower(hash)
		}
		fmt.Fprintf(&hook, "verify '%s' '%s'\n", strings.ReplaceAll(path, "'", "'\\''"), hash)
	}
	dir, err := os.MkdirTemp("", "mcpshka-commit-check-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "pre-commit"), []byte(hook.String()), 0700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
