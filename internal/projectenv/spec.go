package projectenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"mcpshka/internal/failure"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

const Mount = "/env"

// EnvSpec describes how to build one environment.
type EnvSpec struct {
	Kind    string   `json:"kind"` // python, node
	Key     string   `json:"key"`
	Files   []string `json:"files"`
	Command []string `json:"command"`
}

var requirementsFile = regexp.MustCompile(`^requirements[A-Za-z0-9._-]{0,64}\.txt$`)
var envKeyPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// envCommand is the only command an environment build may run.
func envCommand(kind string, files []string) []string {
	switch kind {
	case "python":
		cmd := "python3 -m venv " + Mount + " && " + Mount + "/bin/pip install --no-input --disable-pip-version-check --no-cache-dir"
		for _, f := range files {
			cmd += " -r /workspace/" + f
		}
		return []string{"/bin/sh", "-ec", cmd}
	case "node":
		return []string{"/bin/sh", "-ec", "cp /workspace/package.json /workspace/package-lock.json " + Mount + "/ && cd " + Mount + " && npm ci --ignore-scripts --no-audit --no-fund"}
	}
	return nil
}

func (e *EnvSpec) Valid() bool {
	if e == nil {
		return true
	}
	if !envKeyPattern.MatchString(e.Key) || len(e.Files) == 0 || len(e.Files) > 8 {
		return false
	}
	for _, f := range e.Files {
		switch e.Kind {
		case "python":
			if !requirementsFile.MatchString(f) {
				return false
			}
		case "node":
			if f != "package.json" && f != "package-lock.json" {
				return false
			}
		default:
			return false
		}
	}
	want := envCommand(e.Kind, e.Files)
	if len(want) != len(e.Command) {
		return false
	}
	for i := range want {
		if want[i] != e.Command[i] {
			return false
		}
	}
	return true
}

// detectEnv derives the environment of a commit from its root-level
// dependency files (as listed by ls-tree) and their blob contents.
func Detect(ctx context.Context, repo, commit string, read func(context.Context, string, ...string) ([]byte, error)) (*EnvSpec, error) {
	list, err := read(ctx, repo, "ls-tree", commit)
	if err != nil {
		return nil, failure.ErrHold
	}
	blobs := map[string]string{}
	for _, line := range strings.Split(string(list), "\n") {
		meta, name, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if ok && len(f) == 3 && f[1] == "blob" {
			blobs[name] = f[2]
		}
	}
	var spec EnvSpec
	switch {
	case blobs["package-lock.json"] != "" && blobs["package.json"] != "":
		spec.Kind, spec.Files = "node", []string{"package.json", "package-lock.json"}
	default:
		for name := range blobs {
			if requirementsFile.MatchString(name) {
				spec.Files = append(spec.Files, name)
			}
		}
		if len(spec.Files) == 0 {
			return nil, nil
		}
		sort.Strings(spec.Files)
		if len(spec.Files) > 8 {
			spec.Files = spec.Files[:8]
		}
		spec.Kind = "python"
	}
	h := sha256.New()
	h.Write([]byte(spec.Kind + "\n" + envToolVersion(spec.Kind) + "\n"))
	for _, f := range spec.Files {
		h.Write([]byte(f + "\x00" + blobs[f] + "\n"))
	}
	spec.Key = hex.EncodeToString(h.Sum(nil))[:32]
	spec.Command = envCommand(spec.Kind, spec.Files)
	return &spec, nil
}

// envToolVersion binds the cache to the host interpreter that builds it.
func envToolVersion(kind string) string {
	bin := "/usr/bin/python3"
	if kind == "node" {
		bin = "/usr/bin/node"
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}
