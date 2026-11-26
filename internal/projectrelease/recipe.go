// Package projectrelease is one release path for any project on the host
// (MCP-171, owner 11–12.11.2026: «универсально, вообще для чего угодно»).
// The project describes its own release in .mcpshka/release.json — fixed
// argv steps, no shell; MCPshka runs them the same way for every project.
// The recipe is taken from the released commit and must be the one the owner
// approved with Face ID (its SHA-256 is pinned by a root-owned record), so a
// chat cannot give itself new root commands by editing the file.
package projectrelease

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	Schema     = "mcpshka.release/v1"
	RecipePath = ".mcpshka/release.json"
	ReposRoot  = "/root/repos"
	Broker     = "/usr/local/libexec/mcpshka-broker"
	maxRecipe  = 64 << 10
)

// StateDir holds pins and results; a variable for tests.
var StateDir = "/var/lib/mcpshka-project-release"

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionRe = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`)
	hexRe     = regexp.MustCompile(`^[0-9a-f]+$`)
	stepRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	varRe     = regexp.MustCompile(`\{([a-z][a-z0-9_]{0,31})\}`)
	envRe     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// Step is one fixed command of the release.
type Step struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	// Root runs the step as root; otherwise it runs as the repository's owner.
	Root           bool `json:"root,omitempty"`
	TimeoutSeconds int  `json:"timeout_seconds,omitempty"`
	// Expect, when set, must match the output for the step to pass. With
	// RetrySeconds the command is repeated until Expect matches, Fail
	// matches or the timeout ends (waiting for a gate or an upgrade).
	Expect       string `json:"expect,omitempty"`
	Fail         string `json:"fail,omitempty"`
	RetrySeconds int    `json:"retry_seconds,omitempty"`
	// Capture stores the first match of each expression (its first group
	// when it has one) for later steps as {name}.
	Capture map[string]string `json:"capture,omitempty"`
}

// Recipe is .mcpshka/release.json.
type Recipe struct {
	Schema string `json:"schema"`
	// VersionFile, when set, must hold the released version in the commit.
	VersionFile string            `json:"version_file,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Steps       []Step            `json:"steps"`
	// Rollback runs when a step fails at or after RollbackFrom.
	Rollback     *Step  `json:"rollback,omitempty"`
	RollbackFrom string `json:"rollback_from,omitempty"`
	// RollbackThen runs after a successful rollback, in order (restart the
	// restored service, probe it); a failure there is ROLLBACK_FAILED.
	RollbackThen []Step `json:"rollback_then,omitempty"`
}

func validHex(s string, n int) bool { return len(s) == n && hexRe.MatchString(s) }

// Digest is the identity the owner approves.
func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ParseRecipe is strict: an unknown field is an error, not a silently
// ignored instruction.
func ParseRecipe(raw []byte) (Recipe, error) {
	var r Recipe
	if len(raw) == 0 || len(raw) > maxRecipe {
		return r, errors.New("release recipe: 1 byte..64 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("release recipe: %w", err)
	}
	if r.Schema != Schema || len(r.Steps) == 0 || len(r.Steps) > 24 {
		return r, errors.New("release recipe: schema " + Schema + " and 1..24 steps")
	}
	if f := r.VersionFile; f != "" && (filepath.IsAbs(f) || filepath.Clean(f) != f || strings.HasPrefix(f, "..")) {
		return r, errors.New("release recipe: version_file must be a clean path inside the repository")
	}
	for k, v := range r.Env {
		if !envRe.MatchString(k) || len(v) > 512 || strings.ContainsAny(v, "\x00\n") || k == "PATH" || strings.HasPrefix(k, "LD_") {
			return r, fmt.Errorf("release recipe: env %s is not allowed", k)
		}
	}
	known := map[string]bool{"commit": true, "version": true, "root": true, "name": true}
	seen := map[string]bool{}
	check := func(s Step) error {
		if !stepRe.MatchString(s.Name) || seen[s.Name] || len(s.Argv) == 0 || len(s.Argv) > 32 {
			return fmt.Errorf("step %q: unique lowercase name and 1..32 argv", s.Name)
		}
		seen[s.Name] = true
		if s.TimeoutSeconds < 0 || s.TimeoutSeconds > 3600 || s.RetrySeconds < 0 || s.RetrySeconds > 300 || (s.RetrySeconds > 0 && (s.RetrySeconds < 5 || s.Expect == "")) {
			return fmt.Errorf("step %s: timeout ≤3600 s; retry_seconds 5..300 and only with expect", s.Name)
		}
		if a := s.Argv[0]; !strings.HasPrefix(a, "./") && !filepath.IsAbs(a) || strings.Contains(a, "{") {
			return fmt.Errorf("step %s: argv[0] is ./path inside the repository or an absolute path", s.Name)
		}
		for _, a := range s.Argv {
			if len(a) > 1024 || strings.ContainsRune(a, 0) {
				return fmt.Errorf("step %s: argument too long", s.Name)
			}
			for _, m := range varRe.FindAllStringSubmatch(a, -1) {
				if !known[m[1]] {
					return fmt.Errorf("step %s: {%s} is not captured by an earlier step", s.Name, m[1])
				}
			}
		}
		for _, expr := range []string{s.Expect, s.Fail} {
			if _, err := regexp.Compile(expr); err != nil || len(expr) > 256 {
				return fmt.Errorf("step %s: bad expression", s.Name)
			}
		}
		for k, expr := range s.Capture {
			if _, err := regexp.Compile(expr); err != nil || len(expr) > 256 || !stepRe.MatchString(k) || known[k] {
				return fmt.Errorf("step %s: bad capture %s", s.Name, k)
			}
		}
		for k := range s.Capture {
			known[k] = true
		}
		return nil
	}
	for _, s := range r.Steps {
		if err := check(s); err != nil {
			return r, errors.New("release recipe: " + err.Error())
		}
	}
	if r.Rollback != nil {
		if !seen[r.RollbackFrom] || r.Rollback.RetrySeconds != 0 || len(r.Rollback.Capture) != 0 {
			return r, errors.New("release recipe: rollback needs rollback_from naming a step, no retry, no capture")
		}
		if err := check(*r.Rollback); err != nil {
			return r, errors.New("release recipe: " + err.Error())
		}
		if len(r.RollbackThen) > 8 {
			return r, errors.New("release recipe: rollback_then: at most 8 steps")
		}
		for _, s := range r.RollbackThen {
			if len(s.Capture) != 0 {
				return r, errors.New("release recipe: rollback_then: no capture")
			}
			if err := check(s); err != nil {
				return r, errors.New("release recipe: rollback_then: " + err.Error())
			}
		}
	} else if r.RollbackFrom != "" || len(r.RollbackThen) != 0 {
		return r, errors.New("release recipe: rollback_from/rollback_then without rollback")
	}
	return r, nil
}

// expand substitutes {name} in one argument.
func expand(arg string, vars map[string]string) string {
	return varRe.ReplaceAllStringFunc(arg, func(m string) string { return vars[m[1:len(m)-1]] })
}

// Result is the public report of one release.
type Result struct {
	Project   string `json:"project"`
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Unit      string `json:"unit,omitempty"`
	UnitState string `json:"unit_state,omitempty"`
	// NOT_STARTED, STARTED, RUNNING, INSTALLED, FAILED, ROLLED_BACK,
	// ROLLBACK_FAILED, RECIPE_NOT_APPROVED or REFUSED.
	State        string        `json:"state"`
	Step         string        `json:"step,omitempty"`
	Steps        []StepOutcome `json:"steps,omitempty"`
	RecipeSHA256 string        `json:"recipe_sha256,omitempty"`
	Error        string        `json:"error,omitempty"`
	Next         string        `json:"next,omitempty"`
	FinishedAt   string        `json:"finished_at,omitempty"`
}

type StepOutcome struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Seconds int    `json:"seconds"`
	Tries   int    `json:"tries,omitempty"`
	Tail    string `json:"tail,omitempty"`
}

// Unit names the worker unit of one project version.
func Unit(name, version string) string {
	return "mcpshka-project-release-" + strings.NewReplacer(".", "-", "_", "-").Replace(name) + "-" + strings.NewReplacer(".", "", "-", "").Replace(version)
}

func resultPath(name, version string) string {
	return filepath.Join(StateDir, "results", name, version+".json")
}

func pinPath(name string) string { return filepath.Join(StateDir, "pins", name+".sha256") }

// approveHint is the exact Face ID plan that pins a recipe.
func approveHint(name, sha string) string {
	return fmt.Sprintf(`рецепт выпуска не подтверждён владельцем: approved_task_prepare executable=%s args=["project-release-pin","%s","%s"] workdir=/ network=false timeout_sec=60 write=["%s"] — дай владельцу approval_url (Face ID), после approved_task_status=done повтори project_release с новым ключом`, Broker, name, sha, StateDir)
}
