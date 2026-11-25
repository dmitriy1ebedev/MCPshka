package projectprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mcpshka/internal/statestore"
)

// The registry lets an operator onboard any repository without committing a
// profile into it. Registration is keyed by the Git common directory, so every
// sibling worktree of one repository shares the same profile.
var registry struct {
	sync.RWMutex
	db *statestore.DB
}

// SetRegistry enables registered profiles for every Service in the process.
func SetRegistry(db *statestore.DB) {
	registry.Lock()
	registry.db = db
	registry.Unlock()
}

func registryDB() *statestore.DB {
	registry.RLock()
	defer registry.RUnlock()
	return registry.db
}

// gitCommonDir resolves <root>/.git without running Git: a directory is the
// common dir; a worktree file points at a gitdir whose commondir names it.
func gitCommonDir(root string) (string, error) {
	dotgit := filepath.Join(root, ".git")
	info, err := os.Lstat(dotgit)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return filepath.Clean(dotgit), nil
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("unsupported .git entry")
	}
	raw, err := os.ReadFile(dotgit)
	if err != nil {
		return "", err
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok || gitdir == "" {
		return "", errors.New("invalid .git file")
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return filepath.Clean(gitdir), nil
	}
	dir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(gitdir, dir)
	}
	return filepath.Clean(dir), nil
}

func registryKey(root string) (string, error) {
	common, err := gitCommonDir(root)
	if err != nil {
		return "", fmt.Errorf("registered profiles require a Git repository: %w", err)
	}
	sum := sha256.Sum256([]byte(common))
	return "projects/profiles/" + hex.EncodeToString(sum[:16]) + ".json", nil
}

type registeredProfile struct {
	CommonDir string  `json:"common_dir"`
	Profile   Profile `json:"profile"`
}

func lookupRegistered(root string) (Profile, bool, error) {
	db := registryDB()
	if db == nil {
		return Profile{}, false, nil
	}
	key, err := registryKey(root)
	if err != nil {
		return Profile{}, false, nil
	}
	raw, err := db.Get(key)
	if errors.Is(err, os.ErrNotExist) {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, false, err
	}
	var r registeredProfile
	if err := json.Unmarshal(raw, &r); err != nil {
		return Profile{}, false, fmt.Errorf("registered profile unreadable: %w", err)
	}
	if err := validateProfile(r.Profile); err != nil {
		return Profile{}, false, fmt.Errorf("registered profile invalid: %w", err)
	}
	return r.Profile, true, nil
}

// Register stores (or replaces) the operator-approved profile of a repository.
func (s *Service) Register(path string, profile Profile) (Resolution, error) {
	db := registryDB()
	if db == nil {
		return Resolution{}, errors.New("project registry unavailable: HOLD")
	}
	detected, err := s.projects.Detect(path)
	if err != nil {
		return Resolution{}, err
	}
	if err := validateProfile(profile); err != nil {
		return Resolution{}, err
	}
	// The registry key already binds the repository; a matcher is optional.
	if profile.Match.Module != "" || len(profile.Match.Markers) > 0 {
		if err := validateMatch(profile.Match, detected); err != nil {
			return Resolution{}, fmt.Errorf("profile does not match project: %w", err)
		}
	}
	key, err := registryKey(detected.Root)
	if err != nil {
		return Resolution{}, err
	}
	common, _ := gitCommonDir(detected.Root)
	raw, err := json.Marshal(registeredProfile{CommonDir: common, Profile: profile})
	if err != nil {
		return Resolution{}, err
	}
	if ok, err := db.PutIfAbsent(key, raw); err != nil {
		return Resolution{}, err
	} else if !ok {
		old, err := db.Get(key)
		if err != nil {
			return Resolution{}, err
		}
		if replaced, err := db.Replace(key, old, raw); err != nil || !replaced {
			return Resolution{}, errors.New("registered profile changed concurrently; retry")
		}
	}
	return Resolution{Root: detected.Root, Source: "registered", Profile: cloneProfile(profile)}, nil
}

// Propose drafts a profile from repository markers. It never executes or
// writes anything; the operator reviews and may override every workflow.
func (s *Service) Propose(path string) (Profile, error) {
	detected, err := s.projects.Detect(path)
	if err != nil {
		return Profile{}, err
	}
	name := strings.ToLower(filepath.Base(detected.Root))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	name = b.String()
	if name == "" {
		name = "project"
	}
	if len(name) > maxNameBytes {
		name = name[:maxNameBytes]
	}
	has := map[string]bool{}
	for _, m := range detected.Markers {
		has[m] = true
	}
	exists := func(rel string) bool {
		_, err := os.Lstat(filepath.Join(detected.Root, rel))
		return err == nil
	}
	w := func(command string, args ...string) Workflow {
		return Workflow{Command: command, Args: args, TimeoutSeconds: 1800, OutputLimitBytes: 8 << 20}
	}
	workflows := map[string]Workflow{}
	markers := []string{}
	switch {
	case has["Makefile"] && makeTarget(filepath.Join(detected.Root, "Makefile"), "test"):
		workflows["test"] = w("make", "test")
		markers = append(markers, "Makefile")
	case has["go.mod"]:
		workflows["test"] = w("go", "test", "-p", "1", "./...")
		workflows["check"] = w("go", "vet", "./...")
		markers = append(markers, "go.mod")
	case exists("pytest.ini") || exists("conftest.py") || exists("pyproject.toml"):
		workflows["test"] = w("python3", "-m", "pytest", "-q")
	case exists("tests"):
		workflows["test"] = w("python3", "-m", "unittest", "discover", "-s", "tests")
	case has["package.json"]:
		workflows["test"] = w("npm", "test", "--offline")
		markers = append(markers, "package.json")
	}
	if len(workflows) == 0 {
		return Profile{}, errors.New("no test entrypoint detected; pass an explicit test workflow")
	}
	return Profile{Schema: SchemaV1, Name: name, Description: "Registered workflows for " + name + ".", Match: Match{Markers: markers}, Workflows: workflows}, nil
}

func makeTarget(path, target string) bool {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, target+":") {
			return true
		}
	}
	return false
}

// RecoveryAuthority accepts only operator registration or a built-in profile;
// repository-writable profiles alone cannot expand the root recovery scope.
func RecoveryAuthority(root string, db *statestore.DB) error {
	common, err := gitCommonDir(root)
	if err != nil {
		return err
	}
	if db == nil {
		return errors.New("registered project authority unavailable: HOLD")
	}
	key, err := registryKey(root)
	if err != nil {
		return err
	}
	raw, err := db.Get(key)
	if err == nil {
		var r registeredProfile
		if json.Unmarshal(raw, &r) != nil || r.CommonDir != common || validateProfile(r.Profile) != nil {
			return errors.New("registered Git authority mismatch: HOLD")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	s := New(nil)
	detected, err := s.projects.Detect(root)
	if err != nil || detected.Root != root {
		return errors.New("canonical registered repository required: HOLD")
	}
	for _, profile := range s.builtins {
		if validateMatch(profile.Match, detected) == nil {
			return nil
		}
	}
	return errors.New("operator project registration required: HOLD")
}
