package projectprofile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"mcpshka/internal/project"
)

func readProfile(path string) (Profile, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Profile{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Profile{}, err
	}
	if !info.Mode().IsRegular() {
		return Profile{}, errors.New("profile requires regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxProfileBytes+1))
	if err != nil {
		return Profile{}, err
	}
	if int64(len(data)) > maxProfileBytes {
		return Profile{}, fmt.Errorf("profile exceeds %d-byte limit", maxProfileBytes)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var profile Profile
	if err := dec.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("decode project profile: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Profile{}, errors.New("project profile contains trailing JSON values")
		}
		return Profile{}, fmt.Errorf("decode trailing project profile data: %w", err)
	}
	if err := validateProfile(profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func validateProfile(profile Profile) error {
	if profile.CI != nil && (profile.CI.Provider != "distributed" || !profile.CI.ExternalOnly) {
		return errors.New("CI profile requires distributed external_only")
	}
	if profile.Schema != SchemaV1 {
		return fmt.Errorf("unsupported profile schema %q", profile.Schema)
	}
	if !validToken(profile.Name) {
		return fmt.Errorf("invalid profile name %q", profile.Name)
	}
	if len(profile.Description) > maxDescriptionBytes || strings.ContainsAny(profile.Description, "\x00\r\n") {
		return errors.New("invalid profile description")
	}
	if len(profile.Workflows) == 0 || len(profile.Workflows) > maxWorkflows {
		return fmt.Errorf("workflows must contain 1..%d entries", maxWorkflows)
	}
	for name, workflow := range profile.Workflows {
		if !validToken(name) {
			return fmt.Errorf("invalid workflow name %q", name)
		}
		if err := validateWorkflow(name, workflow); err != nil {
			return err
		}
	}
	if len(profile.Pipelines) > 4 {
		return errors.New("too many pipelines")
	}
	for name, pipeline := range profile.Pipelines {
		if !validToken(name) || len(pipeline.Stages) != len(workflowSteps) {
			return errors.New("invalid pipeline")
		}
		for i, stage := range pipeline.Stages {
			if stage != workflowSteps[i] {
				return errors.New("pipeline must follow the ten workflow steps")
			}
		}
		for _, stage := range append(append([]string(nil), pipeline.Stages...), pipeline.Rollback, pipeline.Finalizer) {
			if _, ok := profile.Workflows[stage]; !ok {
				return fmt.Errorf("pipeline workflow %q missing", stage)
			}
		}
		if pipeline.Rollback == pipeline.Finalizer {
			return errors.New("rollback and finalizer must differ")
		}
		for _, stage := range pipeline.Stages {
			if stage == pipeline.Rollback || stage == pipeline.Finalizer {
				return errors.New("recovery workflows must be separate")
			}
		}
	}
	for _, marker := range profile.Match.Markers {
		if marker == "" || marker != filepath.Base(marker) || marker == "." || marker == ".." {
			return fmt.Errorf("invalid profile marker %q", marker)
		}
	}
	if strings.ContainsAny(profile.Match.Module, "\x00\r\n") {
		return errors.New("invalid module matcher")
	}
	return nil
}

// sealedScript accepts "./dir/script" for the test workflow only: the sealed
// test runs it from the read-only committed snapshot (DecideIsolatedProjectTest),
// so a project's own test gate needs no shell interpreter in its profile.
func sealedScript(name, command string) bool {
	rel, ok := strings.CutPrefix(command, "./")
	if name != "test" || !ok || rel == "" || filepath.Clean(rel) != rel || strings.HasPrefix(rel, "..") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if !validCommand(part) {
			return false
		}
	}
	return true
}

func validateWorkflow(name string, workflow Workflow) error {
	bare := workflow.Command != "" && workflow.Command == filepath.Base(workflow.Command) && validCommand(workflow.Command)
	if !bare && !sealedScript(name, workflow.Command) {
		return fmt.Errorf("workflow %q has invalid command %q", name, workflow.Command)
	}
	if err := ValidateArtifacts(workflow.Artifacts); err != nil {
		return fmt.Errorf("workflow %q: %w", name, err)
	}
	if len(workflow.Args) > maxArgs {
		return fmt.Errorf("workflow %q has too many arguments", name)
	}
	for _, arg := range workflow.Args {
		if len(arg) > maxArgBytes || strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("workflow %q has invalid argument", name)
		}
	}
	if workflow.TimeoutSeconds <= 0 || workflow.TimeoutSeconds > maxTimeoutSeconds {
		return fmt.Errorf("workflow %q timeout_seconds must be 1..%d", name, maxTimeoutSeconds)
	}
	if workflow.OutputLimitBytes <= 0 || workflow.OutputLimitBytes > maxOutputBytes {
		return fmt.Errorf("workflow %q output_limit_bytes must be 1..%d", name, maxOutputBytes)
	}
	if len(workflow.RuntimePaths) > 8 || len(workflow.Env) > 16 {
		return fmt.Errorf("workflow %q has too many runtime_paths or env entries", name)
	}
	for _, p := range workflow.RuntimePaths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsAny(p, "\x00\r\n:") {
			return fmt.Errorf("workflow %q runtime path %q must be a clean absolute path", name, p)
		}
	}
	for k, v := range workflow.Env {
		if !validEnvKey(k) || len(v) > 4096 || strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("workflow %q has invalid env %q", name, k)
		}
	}
	return nil
}

func validateMatch(match Match, detected project.Result) error {
	if match.Module == "" && len(match.Markers) == 0 {
		return errors.New("profile match must declare module or markers")
	}
	if match.Module != "" && detected.Description != match.Module {
		return fmt.Errorf("module=%q want=%q", detected.Description, match.Module)
	}
	have := make(map[string]bool, len(detected.Markers))
	for _, marker := range detected.Markers {
		have[marker] = true
	}
	for _, marker := range match.Markers {
		if !have[marker] {
			if !filepath.IsAbs(detected.Root) {
				return fmt.Errorf("missing marker %q", marker)
			}
			info, err := os.Lstat(filepath.Join(detected.Root, marker))
			if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
				return fmt.Errorf("missing or unsafe marker %q", marker)
			}
		}
	}
	return nil
}

func validToken(value string) bool {
	if value == "" || len(value) > maxNameBytes {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validCommand(value string) bool {
	if value == "" || len(value) > maxNameBytes {
		return false
	}
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '+' {
			continue
		}
		return false
	}
	return true
}

func cloneProfile(in Profile) Profile {
	out := in
	if in.CI != nil {
		c := *in.CI
		out.CI = &c
	}
	out.Match.Markers = append([]string(nil), in.Match.Markers...)
	out.Workflows = make(map[string]Workflow, len(in.Workflows))
	for name, workflow := range in.Workflows {
		workflow.Artifacts = append([]string(nil), workflow.Artifacts...)
		workflow.Args = append([]string(nil), workflow.Args...)
		workflow.RuntimePaths = append([]string(nil), workflow.RuntimePaths...)
		if workflow.Env != nil {
			env := make(map[string]string, len(workflow.Env))
			for k, v := range workflow.Env {
				env[k] = v
			}
			workflow.Env = env
		}
		out.Workflows[name] = workflow
	}
	out.Pipelines = make(map[string]Pipeline, len(in.Pipelines))
	for name, pipeline := range in.Pipelines {
		pipeline.Stages = append([]string(nil), pipeline.Stages...)
		out.Pipelines[name] = pipeline
	}
	return out
}

func validEnvKey(k string) bool {
	if k == "" || len(k) > 64 || k[0] < 'A' || k[0] > 'Z' {
		return false
	}
	for _, r := range k {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
