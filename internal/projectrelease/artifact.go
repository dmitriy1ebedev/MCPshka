package projectrelease

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"mcpshka/internal/ciartifact"
	"mcpshka/internal/pipeline"
)

var artifactRef = regexp.MustCompile(`\{artifact:([^{}]*)\}`)

// ExpandArtifacts substitutes only recipe-authored artifact references, before captured values.
func ExpandArtifacts(repo, head, arg string) (string, error) {
	var failure error
	out := artifactRef.ReplaceAllStringFunc(arg, func(ref string) string {
		rel := ref[len("{artifact:") : len(ref)-1]
		var path string
		var err error
		if strings.HasPrefix(rel, "out/distributed/") {
			path, _, err = ciartifact.Resolve(repo, head, rel)
		} else {
			path, _, err = pipeline.ResolveArtifactAt(repo, head, rel)
		}
		if err != nil {
			failure = err
		}
		return path
	})
	if failure != nil {
		return "", failure
	}
	if strings.Contains(out, "{artifact:") {
		return "", errors.New("malformed artifact reference")
	}
	return out, nil
}

func externalRecipe(recipe Recipe) error {
	hasArtifact := false
	for _, step := range recipe.Steps {
		if len(step.Argv) == 0 {
			return errors.New("empty external install step")
		}
		switch filepath.Base(step.Argv[0]) {
		case "go", "make", "gcc", "cc", "npm", "npx", "cargo", "python", "python3", "sh", "bash":
			return errors.New("external-only CI profile refuses host build/test interpreters; install verified CI artifacts")
		}
		for _, arg := range step.Argv {
			if strings.Contains(arg, "{artifact:out/distributed/") {
				hasArtifact = true
			}
		}
	}
	if !hasArtifact {
		return errors.New("external-only CI profile requires {artifact:out/distributed/...}; run forge_ci provider=distributed before installation")
	}
	return nil
}
