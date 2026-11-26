// Package approvedtask resolves tool-side references before the broker sees a plan.
package approvedtask

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"mcpshka/internal/approvedplan"
	"mcpshka/internal/pipeline"
)

// ResolveArtifacts pins pipeline: arguments as inputs too, even if omitted from inputs.
func ResolveArtifacts(p approvedplan.Plan) (approvedplan.Plan, error) {
	p.Args, p.Inputs, p.InputSHAs = slices.Clone(p.Args), slices.Clone(p.Inputs), maps.Clone(p.InputSHAs)
	if p.InputSHAs == nil {
		p.InputSHAs = map[string]string{}
	}
	var repo, head string
	resolve := func(ref string) (string, error) {
		if !strings.HasPrefix(ref, "pipeline:") {
			return ref, nil
		}
		if repo == "" {
			var err error
			repo, head, err = pipeline.RepositoryHead(p.Workdir)
			if err != nil {
				return "", err
			}
		}
		path, sha, err := pipeline.ResolveArtifactAt(repo, head, strings.TrimPrefix(ref, "pipeline:"))
		if err != nil {
			return "", err
		}
		if old := p.InputSHAs[ref]; old != "" && old != sha {
			return "", fmt.Errorf("pipeline input SHA-256 mismatch: %s", ref)
		}
		if old := p.InputSHAs[path]; old != "" && old != sha {
			return "", fmt.Errorf("pipeline input SHA-256 mismatch: %s", path)
		}
		delete(p.InputSHAs, ref)
		p.InputSHAs[path] = sha
		return path, nil
	}
	for i, ref := range p.Inputs {
		path, err := resolve(ref)
		if err != nil {
			return p, err
		}
		p.Inputs[i] = path
	}
	for i, ref := range p.Args {
		path, err := resolve(ref)
		if err != nil {
			return p, err
		}
		p.Args[i] = path
		if strings.HasPrefix(ref, "pipeline:") && !slices.Contains(p.Inputs, path) {
			p.Inputs = append(p.Inputs, path)
		}
	}
	return p, p.ValidateDowngrade()
}
