package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PlanTargetedTest uses Go directly when filters are requested, even with a Makefile.
func (s *Service) PlanTargetedTest(path string, packages []string, run string) (TestPlan, error) {
	if len(packages) == 0 && run == "" {
		plan, err := s.PlanTest(path)
		if err == nil {
			plan = changedTest(plan)
		}
		return plan, err
	}
	if len(packages) > 128 || len(run) > 4096 {
		return TestPlan{}, fmt.Errorf("test filters exceed budget")
	}
	if _, err := regexp.Compile(run); err != nil {
		return TestPlan{}, fmt.Errorf("invalid run regex: %w", err)
	}
	for _, pkg := range packages {
		if pkg == "." {
			continue
		}
		if len(pkg) > 512 || !strings.HasPrefix(pkg, "./") {
			return TestPlan{}, fmt.Errorf("package must be module-relative: %q", pkg)
		}
		parts := strings.Split(strings.TrimPrefix(pkg, "./"), "/")
		for i, part := range parts {
			if part == "..." && i == len(parts)-1 {
				continue
			}
			if part == "" || part == "." || part == ".." {
				return TestPlan{}, fmt.Errorf("invalid package: %q", pkg)
			}
			for _, c := range part {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
					return TestPlan{}, fmt.Errorf("invalid package: %q", pkg)
				}
			}
		}
	}
	detected, err := s.Detect(path)
	if err != nil {
		return TestPlan{}, err
	}
	if !fileExists(filepath.Join(detected.Root, "go.mod")) {
		return TestPlan{}, fmt.Errorf("test filters require a Go module")
	}
	for _, pkg := range packages {
		resolved, err := filepath.EvalSymlinks(filepath.Join(detected.Root, strings.TrimSuffix(pkg, "/...")))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return TestPlan{}, err
		}
		relative, err := filepath.Rel(detected.Root, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return TestPlan{}, fmt.Errorf("package escapes module: %q", pkg)
		}
	}
	args := []string{"test", "-p", "1"}
	if run != "" {
		args = append(args, "-run", run)
	}
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	args = append(args, packages...)
	return TestPlan{Root: detected.Root, Kind: "go", Command: "go", Args: args, Reason: "explicit Go test filters; one package at a time"}, nil
}

// GoTestNoTimeout turns off go test's own 10-minute panic: the job watchdog
// scales the budget with H96 load and extends it on progress, so a second,
// fixed clock only killed healthy suites on a loaded box.
func GoTestNoTimeout(plan TestPlan) []string {
	// A registered profile test runs sealed only with its exact registered
	// argv; an added flag turned go2rtc's Go profile into an ordinary job
	// with no HEAD receipt for phase_gate (MCP-270).
	if plan.Kind == "profile" || plan.Command != "go" || len(plan.Args) == 0 || plan.Args[0] != "test" {
		return plan.Args
	}
	for _, a := range plan.Args {
		if a == "-timeout" || strings.HasPrefix(a, "-timeout=") {
			return plan.Args
		}
	}
	return append([]string{"test", "-timeout=0"}, plan.Args[1:]...)
}
