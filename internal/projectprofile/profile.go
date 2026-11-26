package projectprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcpshka/internal/policy"
	"mcpshka/internal/project"
)

const (
	SchemaV1                  = "mcpshka.project-profile/v1"
	localProfilePath          = ".mcpshka/profile.json"
	maxProfileBytes     int64 = 64 * 1024
	maxWorkflows              = 16
	maxArgs                   = 64
	maxArgBytes               = 4096
	maxNameBytes              = 64
	maxDescriptionBytes       = 512
	maxTimeoutSeconds         = 21600
	maxOutputBytes      int64 = 64 * 1024 * 1024
)

type Match struct {
	Module  string   `json:"module,omitempty"`
	Markers []string `json:"markers,omitempty"`
}

type Workflow struct {
	Artifacts        []string `json:"artifacts,omitempty"`
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	TimeoutSeconds   int64    `json:"timeout_seconds,omitempty"`
	OutputLimitBytes int64    `json:"output_limit_bytes,omitempty"`
	// RuntimePaths are host directories outside the commit a sealed test
	// needs (a project venv); root admits only owner-allowlisted prefixes.
	RuntimePaths []string          `json:"runtime_paths,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}

// SealedArgv makes the managed job deadline authoritative for Go test
// workflows with no explicit package timeout.
func (w Workflow) SealedArgv(stage string) []string {
	args := w.Args
	if stage == "test" {
		args = project.GoTestNoTimeout(project.TestPlan{Command: w.Command, Args: w.Args})
	}
	return append([]string{w.Command}, args...)
}

type Profile struct {
	CI          *CI                 `json:"ci,omitempty"`
	Schema      string              `json:"schema"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Match       Match               `json:"match,omitempty"`
	Workflows   map[string]Workflow `json:"workflows"`
	Pipelines   map[string]Pipeline `json:"pipelines,omitempty"`
}

// Pipeline references trusted workflows; it never contains executable evidence.
type Pipeline struct {
	Stages    []string `json:"stages"`
	Rollback  string   `json:"rollback"`
	Finalizer string   `json:"finalizer"`
}

type Resolution struct {
	Root    string  `json:"root"`
	Source  string  `json:"source"`
	Profile Profile `json:"profile"`
}

type Plan struct {
	Fingerprint      string   `json:"fingerprint"`
	Root             string   `json:"root"`
	Profile          string   `json:"profile"`
	Source           string   `json:"source"`
	Workflow         string   `json:"workflow"`
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	TimeoutSeconds   int64    `json:"timeout_seconds"`
	OutputLimitBytes int64    `json:"output_limit_bytes"`
}

type Service struct {
	policy   *policy.Policy
	projects *project.Service
	builtins []Profile
}

func New(p *policy.Policy) *Service {
	if p == nil {
		p = policy.Default()
	}
	return &Service{
		policy:   p,
		projects: project.New(p),
		builtins: []Profile{diagProfile(), laneProfile(), mcpshkaProfile()},
	}
}

func (s *Service) Resolve(path string) (Resolution, error) {
	detected, err := s.projects.Detect(path)
	if err != nil {
		return Resolution{}, err
	}
	root := detected.Root

	local := filepath.Join(root, localProfilePath)
	if info, err := os.Lstat(local); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return Resolution{}, fmt.Errorf("%s must be a regular non-symlink file", localProfilePath)
		}
		profile, err := readProfile(local)
		if err != nil {
			return Resolution{}, err
		}
		if err := validateMatch(profile.Match, detected); err != nil {
			return Resolution{}, fmt.Errorf("repository profile does not match project: %w", err)
		}
		return Resolution{Root: root, Source: "repository", Profile: profile}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Resolution{}, err
	}

	if profile, ok, err := lookupRegistered(root); err != nil {
		return Resolution{}, err
	} else if ok {
		if profile.Match.Module != "" || len(profile.Match.Markers) > 0 {
			if err := validateMatch(profile.Match, detected); err != nil {
				return Resolution{}, fmt.Errorf("registered profile does not match project: %w", err)
			}
		}
		return Resolution{Root: root, Source: "registered", Profile: profile}, nil
	}

	for _, profile := range s.builtins {
		if validateMatch(profile.Match, detected) == nil {
			return Resolution{Root: root, Source: "builtin", Profile: cloneProfile(profile)}, nil
		}
	}
	return Resolution{}, fmt.Errorf("no reusable project profile matched %s", root)
}

func (s *Service) Plan(path, workflow string) (Plan, error) {
	workflow = strings.TrimSpace(workflow)
	if !validToken(workflow) {
		return Plan{}, fmt.Errorf("invalid workflow name %q", workflow)
	}
	resolved, err := s.Resolve(path)
	if err != nil {
		return Plan{}, err
	}
	return planResolved(resolved, workflow)
}

// PipelinePlans resolves the profile once so one stage poll does not repeat
// twelve Git/project probes. All references were checked by validateProfile.
func (s *Service) PipelinePlans(path, name string) (Resolution, []Plan, error) {
	resolved, err := s.Resolve(path)
	if err != nil {
		return resolved, nil, err
	}
	p, ok := resolved.Profile.Pipelines[name]
	if !ok {
		return resolved, nil, errors.New("pipeline not defined by trusted profile")
	}
	var plans []Plan
	for _, stage := range append(append([]string(nil), p.Stages...), p.Rollback, p.Finalizer) {
		plan, err := planResolved(resolved, stage)
		if err != nil {
			return resolved, nil, err
		}
		plans = append(plans, plan)
	}
	return resolved, plans, nil
}

func (r Resolution) Plan(workflow string) (Plan, error) { return planResolved(r, workflow) }

func planResolved(resolved Resolution, workflow string) (Plan, error) {
	item, ok := resolved.Profile.Workflows[workflow]
	if !ok {
		names := make([]string, 0, len(resolved.Profile.Workflows))
		for name := range resolved.Profile.Workflows {
			names = append(names, name)
		}
		sort.Strings(names)
		return Plan{}, fmt.Errorf("workflow %q is not defined by profile %q; available=%s", workflow, resolved.Profile.Name, strings.Join(names, ","))
	}
	plan := Plan{
		Root:             resolved.Root,
		Profile:          resolved.Profile.Name,
		Source:           resolved.Source,
		Workflow:         workflow,
		Command:          item.Command,
		Args:             append([]string(nil), item.Args...),
		TimeoutSeconds:   item.TimeoutSeconds,
		OutputLimitBytes: item.OutputLimitBytes,
	}
	// Bind an AI handoff to the exact resolved execution plan. The hash
	// excludes itself and changes whenever command, arguments or limits change.
	encoded, err := json.Marshal(plan)
	if err != nil {
		return Plan{}, fmt.Errorf("encode workflow plan: %w", err)
	}
	digest := sha256.Sum256(encoded)
	plan.Fingerprint = hex.EncodeToString(digest[:])
	return plan, nil
}
