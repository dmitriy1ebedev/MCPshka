package projectprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
)

var StageOrder = []string{"prepare", "build", "test", "lint", "package", "verify"}

func ValidStage(stage string) bool { return slices.Contains(StageOrder, stage) }

// StageSelection is canonical: callers cannot reorder or silently skip stages.
func StageSelection(stages []string) ([]string, error) {
	if len(stages) == 0 || slices.Equal(stages, []string{"all"}) {
		return slices.Clone(StageOrder), nil
	}
	last := -1
	for _, stage := range stages {
		i := slices.Index(StageOrder, stage)
		if i <= last {
			return nil, errors.New("stages must be unique prepare/build/test/lint/package/verify in order, or all")
		}
		last = i
	}
	return slices.Clone(stages), nil
}

func ValidateArtifacts(paths []string) error {
	if len(paths) > 16 {
		return errors.New("at most 16 artifact files")
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || p == "." || p == "manifest.json" || len(p) > 512 || filepath.Clean(p) != p || filepath.IsAbs(p) || strings.ContainsAny(p, "\\\x00\r\n") || seen[p] {
			return errors.New("artifact must be a unique clean relative file path")
		}
		for _, part := range strings.Split(p, "/") {
			if part == ".." || part == ".git" {
				return errors.New("artifact outside project")
			}
		}
		seen[p] = true
	}
	return nil
}

// ProfileDigest includes commands, limits, runtime dependencies and artifact configuration.
func ProfileDigest(p Profile) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
