package project

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"mcpshka/internal/gitops"
	"mcpshka/internal/policy"
)

type Service struct {
	policy *policy.Policy
	git    *gitops.Service
}

func New(p *policy.Policy) *Service {
	if p == nil {
		p = policy.Default()
	}
	return &Service{policy: p, git: gitops.New(p)}
}

type Result struct {
	Path        string   "json:\"path\""
	Root        string   "json:\"root,omitempty\""
	Name        string   "json:\"name\""
	Kinds       []string "json:\"kinds\""
	Git         bool     "json:\"git\""
	Branch      string   "json:\"branch,omitempty\""
	Dirty       bool     "json:\"dirty,omitempty\""
	Markers     []string "json:\"markers,omitempty\""
	Description string   "json:\"description,omitempty\""
}

func (s *Service) Detect(path string) (Result, error) {
	real, err := s.policy.ResolveExisting(path)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return Result{}, err
	}
	base := real
	if !info.IsDir() {
		base = filepath.Dir(real)
	}

	result := Result{Path: real, Root: base, Name: filepath.Base(base)}
	baseHasMarker := hasProjectMarker(base)
	if root, err := s.git.RepoRoot(base); err == nil {
		result.Git = true
		if !baseHasMarker {
			result.Root = root
			result.Name = filepath.Base(root)
		}
		if status, err := s.git.Status(root, 16*1024); err == nil {
			result.Branch = status.Branch
			result.Dirty = status.Dirty
		}
	}

	markers := []struct {
		name string
		kind string
	}{
		{"go.mod", "go"},
		{"package.json", "node"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"Cargo.toml", "rust"},
		{"Makefile", "make"},
		{"CMakeLists.txt", "cmake"},
		{"Dockerfile", "docker"},
		{"docker-compose.yml", "docker-compose"},
		{"compose.yml", "docker-compose"},
	}
	seen := map[string]bool{}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(result.Root, marker.name)); err == nil {
			result.Markers = append(result.Markers, marker.name)
			if !seen[marker.kind] {
				seen[marker.kind] = true
				result.Kinds = append(result.Kinds, marker.kind)
			}
		}
	}
	if b, err := readSmallFile(filepath.Join(result.Root, "go.mod"), 65536); err == nil {
		line := strings.SplitN(string(b), "\n", 2)[0]
		if strings.HasPrefix(line, "module ") {
			result.Description = strings.TrimPrefix(line, "module ")
		}
	}
	return result, nil
}

type TestPlan struct {
	Root    string   "json:\"root\""
	Kind    string   "json:\"kind\""
	Command string   "json:\"command\""
	Args    []string "json:\"args\""
	Reason  string   "json:\"reason\""
}

func (s *Service) PlanTest(path string) (TestPlan, error) {
	detected, err := s.Detect(path)
	if err != nil {
		return TestPlan{}, err
	}
	root := detected.Root

	if hasMakeTestTarget(filepath.Join(root, "Makefile")) {
		return TestPlan{
			Root: root, Kind: "make", Command: "make", Args: []string{"test"},
			Reason: "Makefile defines a test target",
		}, nil
	}

	if plan, ok := nodeTestPlan(root); ok {
		return plan, nil
	}

	if fileExists(filepath.Join(root, "go.mod")) {
		return TestPlan{
			Root: root, Kind: "go", Command: "go", Args: []string{"test", "-p", "1", "./..."},
			Reason: "go.mod detected; one package at a time to stay within the job memory limit",
		}, nil
	}

	if fileExists(filepath.Join(root, "Cargo.toml")) {
		return TestPlan{
			Root: root, Kind: "rust", Command: "cargo", Args: []string{"test"},
			Reason: "Cargo.toml detected",
		}, nil
	}

	if pythonPytestProject(root) {
		return TestPlan{
			Root: root, Kind: "python-pytest", Command: "python3", Args: []string{"-m", "pytest", "-q"},
			Reason: "pytest markers detected",
		}, nil
	}
	if dirExists(filepath.Join(root, "tests")) {
		return TestPlan{
			Root: root, Kind: "python-unittest", Command: "python3",
			Args:   []string{"-m", "unittest", "discover", "-s", "tests"},
			Reason: "tests directory detected without pytest markers",
		}, nil
	}

	return TestPlan{}, fmt.Errorf("no safe automatic test strategy detected for %s", root)
}

func nodeTestPlan(root string) (TestPlan, bool) {
	path := filepath.Join(root, "package.json")
	b, err := readSmallFile(path, 1024*1024)
	if err != nil {
		return TestPlan{}, false
	}
	var pkg struct {
		Scripts map[string]string "json:\"scripts\""
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return TestPlan{}, false
	}
	script := strings.TrimSpace(pkg.Scripts["test"])
	if script == "" || strings.Contains(script, "Error: no test specified") {
		return TestPlan{}, false
	}
	return TestPlan{
		Root: root, Kind: "node", Command: "npm", Args: []string{"test", "--silent"},
		Reason: "package.json defines scripts.test",
	}, true
}

func pythonPytestProject(root string) bool {
	for _, name := range []string{"pytest.ini", "conftest.py"} {
		if fileExists(filepath.Join(root, name)) {
			return true
		}
	}
	for _, name := range []string{"pyproject.toml", "requirements.txt", "requirements-dev.txt"} {
		b, err := readSmallFile(filepath.Join(root, name), 1024*1024)
		if err == nil && strings.Contains(strings.ToLower(string(b)), "pytest") {
			return true
		}
	}
	return false
}

func hasMakeTestTarget(path string) bool {
	b, err := readSmallFile(path, 1024*1024)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "test:") || strings.HasPrefix(line, "test :") {
			return true
		}
	}
	return false
}

func readSmallFile(path string, max int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("planning requires regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("file exceeds %d-byte planning limit", max)
	}
	return b, nil
}

func hasProjectMarker(root string) bool {
	for _, name := range []string{
		"go.mod", "package.json", "pyproject.toml", "requirements.txt",
		"Cargo.toml", "Makefile", "CMakeLists.txt", "Dockerfile",
		"docker-compose.yml", "compose.yml",
	} {
		if fileExists(filepath.Join(root, name)) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

type BuildPlan struct {
	Root    string   "json:\"root\""
	Kind    string   "json:\"kind\""
	Command string   "json:\"command\""
	Args    []string "json:\"args\""
	Reason  string   "json:\"reason\""
}

func (s *Service) PlanBuild(path string) (BuildPlan, error) {
	detected, err := s.Detect(path)
	if err != nil {
		return BuildPlan{}, err
	}
	root := detected.Root

	if hasMakeTarget(filepath.Join(root, "Makefile"), "build") {
		return BuildPlan{
			Root: root, Kind: "make", Command: "make", Args: []string{"build"},
			Reason: "Makefile defines a build target",
		}, nil
	}

	if plan, ok := nodeBuildPlan(root); ok {
		return plan, nil
	}

	if fileExists(filepath.Join(root, "go.mod")) {
		return BuildPlan{
			Root: root, Kind: "go", Command: "go", Args: []string{"build", "-p", "1", "-buildvcs=false", "./..."},
			Reason: "go.mod detected; VCS stamping disabled for deterministic isolated builds",
		}, nil
	}

	if fileExists(filepath.Join(root, "Cargo.toml")) {
		return BuildPlan{
			Root: root, Kind: "rust", Command: "cargo", Args: []string{"build"},
			Reason: "Cargo.toml detected",
		}, nil
	}

	return BuildPlan{}, fmt.Errorf("no safe automatic build strategy detected for %s", root)
}

func nodeBuildPlan(root string) (BuildPlan, bool) {
	path := filepath.Join(root, "package.json")
	b, err := readSmallFile(path, 1024*1024)
	if err != nil {
		return BuildPlan{}, false
	}
	var pkg struct {
		Scripts map[string]string "json:\"scripts\""
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return BuildPlan{}, false
	}
	if strings.TrimSpace(pkg.Scripts["build"]) == "" {
		return BuildPlan{}, false
	}
	return BuildPlan{
		Root: root, Kind: "node", Command: "npm", Args: []string{"run", "build", "--silent"},
		Reason: "package.json defines scripts.build",
	}, true
}

func hasMakeTarget(path, target string) bool {
	b, err := readSmallFile(path, 1024*1024)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, target+":") || strings.HasPrefix(line, target+" :") {
			return true
		}
	}
	return false
}

type VerifyPlan struct {
	Root    string   "json:\"root\""
	Kind    string   "json:\"kind\""
	Command string   "json:\"command\""
	Args    []string "json:\"args\""
	Reason  string   "json:\"reason\""
}

func (s *Service) PlanVerify(path string) (VerifyPlan, error) {
	detected, err := s.Detect(path)
	if err != nil {
		return VerifyPlan{}, err
	}
	root := detected.Root

	if hasMakeTarget(filepath.Join(root, "Makefile"), "verify") {
		return VerifyPlan{
			Root: root, Kind: "make", Command: "make", Args: []string{"verify"},
			Reason: "Makefile defines an explicit verify target",
		}, nil
	}
	if plan, ok := nodeVerifyPlan(root); ok {
		return plan, nil
	}
	return VerifyPlan{}, fmt.Errorf("no explicit project verification contract detected for %s", root)
}

func nodeVerifyPlan(root string) (VerifyPlan, bool) {
	path := filepath.Join(root, "package.json")
	b, err := readSmallFile(path, 1024*1024)
	if err != nil {
		return VerifyPlan{}, false
	}
	var pkg struct {
		Scripts map[string]string "json:\"scripts\""
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return VerifyPlan{}, false
	}
	if strings.TrimSpace(pkg.Scripts["verify"]) == "" {
		return VerifyPlan{}, false
	}
	return VerifyPlan{
		Root: root, Kind: "node", Command: "npm", Args: []string{"run", "verify", "--silent"},
		Reason: "package.json defines scripts.verify",
	}, true
}
