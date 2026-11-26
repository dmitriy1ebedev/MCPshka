package gitops

import (
	"errors"
	"os/exec"
	"strings"
	"time"
)

type Remote struct {
	Name string
	URL  string `json:"-"`
}

// Remotes reads only local configuration; it never contacts a provider.
func (s *Service) Remotes(path string) ([]Remote, error) {
	root, err := s.RepoRoot(path)
	if err != nil {
		return nil, err
	}
	raw, _, _, truncated, err := runBounded(root, 3*time.Second, 8192, "git", "config", "--local", "--get-regexp", "^remote\\..*\\.url$")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return []Remote{}, nil
	}
	if err != nil || truncated {
		return nil, errors.New("Метаданные Git remote недоступны")
	}
	out := []Remote{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, " ")
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if !ok || name == "" || len(name) > 128 || len(value) > 1024 || len(out) == 32 {
			return nil, errors.New("Метаданные Git remote превышают лимит")
		}
		out = append(out, Remote{Name: name, URL: value})
	}
	return out, nil
}
