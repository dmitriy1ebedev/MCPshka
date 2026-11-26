package projectops

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

func artifactTracked(ctx context.Context, root string) (map[string]bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory=*", "-C", root, "ls-files", "-z", "--cached")
	raw, err := cmd.Output()
	if err != nil || len(raw) > 4<<20 {
		return nil, errors.New("cannot inventory tracked files")
	}
	out := map[string]bool{}
	for _, p := range strings.Split(string(raw), "\x00") {
		out[p] = true
	}
	return out, nil
}
