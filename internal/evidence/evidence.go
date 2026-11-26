// Package evidence keeps the latest gate result per project, bound to the
// inputs it proved: commit, tree, version, artifact SHA, config digest and
// device identity. A record whose inputs no longer match is stale, never
// PASS; a step is DONE only when every gate it requires holds valid PASS.
package evidence

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var Gates = []string{"integration", "cleanup", "engineering", "build", "packaging", "native", "docker", "device", "release", "deploy", "acceptance", "browser", "delivery"}

type Inputs struct {
	Head         string `json:"head,omitempty"`
	Tree         string `json:"tree,omitempty"`
	Version      string `json:"version,omitempty"`
	ArtifactSHA  string `json:"artifact_sha,omitempty"`
	ConfigDigest string `json:"config_digest,omitempty"`
	Device       string `json:"device,omitempty"`
}

type Record struct {
	Project string    `json:"project"`
	Scope   string    `json:"scope,omitempty"`
	Gate    string    `json:"gate"`
	Result  string    `json:"result"` // PASS, FAIL
	Inputs  Inputs    `json:"inputs"`
	Source  string    `json:"source"` // tool that produced it
	Ref     string    `json:"ref,omitempty"`
	At      time.Time `json:"at"`
}

// Dir is swapped by tests.
var Dir = "/var/lib/mcpshka/evidence"

func path(project, gate string, scope ...string) string {
	if len(scope) > 0 && scope[0] != "" {
		s := sha256.Sum256([]byte(scope[0]))
		gate += "-" + hex.EncodeToString(s[:])
	}
	sum := sha256.Sum256([]byte(project))
	return filepath.Join(Dir, hex.EncodeToString(sum[:8]), gate+".json")
}

func Put(r Record) error {
	if !slices.Contains(Gates, r.Gate) || (r.Result != "PASS" && r.Result != "FAIL") || r.Project == "" {
		return errors.New("evidence: unknown gate, result or project")
	}
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	p := path(r.Project, r.Gate, r.Scope)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".receipt-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, p); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func Get(project, gate string, scope ...string) (Record, bool) {
	var r Record
	raw, err := os.ReadFile(path(project, gate, scope...))
	return r, err == nil && json.Unmarshal(raw, &r) == nil
}

type GateState struct {
	Gate   string  `json:"gate"`
	State  string  `json:"state"` // PASS, FAIL, STALE, MISSING
	Reason string  `json:"reason,omitempty"`
	Record *Record `json:"record,omitempty"`
}

// stale names the first recorded input that differs from now; empty inputs
// on either side are not compared (a gate records what it proved).
func stale(rec, now Inputs) string {
	pairs := [][3]string{{"head", rec.Head, now.Head}, {"tree", rec.Tree, now.Tree}, {"version", rec.Version, now.Version}, {"artifact_sha", rec.ArtifactSHA, now.ArtifactSHA}, {"config_digest", rec.ConfigDigest, now.ConfigDigest}, {"device", rec.Device, now.Device}}
	for _, p := range pairs {
		if p[1] != "" && p[2] != "" && p[1] != p[2] {
			return p[0] + " changed"
		}
	}
	return ""
}

// Evaluate reports each required gate against the current inputs.
func Evaluate(project string, required []string, now Inputs, scope ...string) (states []GateState, done bool) {
	done = len(required) > 0
	for _, g := range required {
		s := GateState{Gate: g, State: "MISSING"}
		if rec, ok := Get(project, g, scope...); ok {
			s.Record = &rec
			switch why := stale(rec.Inputs, now); {
			case why != "":
				s.State, s.Reason = "STALE", why
			default:
				s.State = rec.Result
			}
		}
		done = done && s.State == "PASS"
		states = append(states, s)
	}
	return states, done
}

var gatesLine = regexp.MustCompile(`(?i)^\s*(?:[-*]\s*)?\**gates\**\s*:\s*(.+)$`)

// RequiredGates reads the step's machine criteria from a ROADMAP: the first
// "Gates: a, b" line under the heading that names step, before the next
// heading of the same or a higher level.
func RequiredGates(roadmap, step string) ([]string, error) {
	f, err := os.Open(roadmap)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	level := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256<<10), 256<<10)
	for sc.Scan() {
		line := sc.Text()
		if h := len(line) - len(strings.TrimLeft(line, "#")); h > 0 && strings.HasPrefix(line[h:], " ") {
			if level > 0 && h <= level {
				break
			}
			if level == 0 && strings.Contains(line, step) {
				level = h
			}
			continue
		}
		if level == 0 {
			continue
		}
		if m := gatesLine.FindStringSubmatch(line); m != nil {
			var out []string
			for _, g := range strings.Split(m[1], ",") {
				g = strings.ToLower(strings.Trim(g, " \t`*."))
				if !slices.Contains(Gates, g) {
					return nil, errors.New("unknown gate " + g)
				}
				out = append(out, g)
			}
			return out, nil
		}
	}
	if level == 0 {
		return nil, errors.New("step not found in ROADMAP")
	}
	return nil, nil
}
