package pipeline

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Exports is the private store of verified project_pipeline exports.
var Exports = "/var/lib/mcpshka/pipeline"

func noArtifactAliases(path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if real != filepath.Clean(path) {
		return errors.New("pipeline artifact symlink rejected")
	}
	return nil
}

// ResolveArtifact resolves only a repository's current HEAD, never an arbitrary store path.
func ResolveArtifact(repo, rel string) (string, string, error) {
	root, head, err := RepositoryHead(repo)
	if err != nil {
		return "", "", err
	}
	return ResolveArtifactAt(root, head, rel)
}

// RepositoryHead identifies one repository snapshot for a group of artifact references.
func RepositoryHead(repo string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--show-toplevel", "HEAD").Output()
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(parts) != 2 {
		return "", "", errors.New("cannot identify artifact repository HEAD")
	}
	return parts[0], parts[1], nil
}

// ResolveArtifactAt is for consumers whose repository and commit are already pinned.
func ResolveArtifactAt(repo, head, rel string) (string, string, error) {
	path, m, err := PackagedArtifact(Exports, repo, head, rel)
	if err != nil {
		return "", "", err
	}
	for _, a := range m.Artifacts {
		if a.Path == rel {
			return path, a.SHA256, nil
		}
	}
	return "", "", errors.New("verified export lost artifact identity")
}
