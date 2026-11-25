package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/projectprofile"
	"os"
	"path/filepath"
)

// PackagedArtifact returns the newest verified package export of project at
// head that carries rel, so an installer takes the sealed build itself
// (14.11: the IPK sat in the pipeline store, webos_deploy looked only in the
// repo and the chat spent half an hour on "no approved export").
func PackagedArtifact(base, project, head, rel string) (file string, best PipelineManifest, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w; next_safe_action = project_pipeline path=%s stages=[package], then retry with the same path %s", rel, err, project, rel)
		}
	}()
	if err = projectprofile.ValidateArtifacts([]string{rel}); err != nil {
		return "", best, err
	}
	if err = noArtifactAliases(base); err != nil {
		return "", best, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", best, err
	}
	if len(entries) > 1000 {
		return "", best, errors.New("pipeline export scan exceeded")
	}
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) != 64 {
			continue
		}
		path := filepath.Join(base, e.Name(), "manifest.json")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 || noArtifactAliases(path) != nil {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) > 65536 {
			continue
		}
		var m PipelineManifest
		if json.Unmarshal(raw, &m) != nil || m.Schema != "mcpshka.pipeline/v1" || m.Stage != "package" || m.State != "PASS" || m.Project != project || m.HEAD != head || m.ManifestPath != path || pipelineIdentity(m) != e.Name() {
			continue
		}
		for _, a := range m.Artifacts {
			if a.Path == rel && m.FinishedAt.After(best.FinishedAt) {
				best = m
			}
		}
	}
	if best.ManifestPath == "" {
		return "", best, errors.New("no PASS package export of this HEAD carries it")
	}
	if err := noArtifactAliases(best.ManifestPath); err != nil {
		return "", best, err
	}
	for _, a := range best.Artifacts {
		if err := noArtifactAliases(filepath.Join(filepath.Dir(best.ManifestPath), a.Path)); err != nil {
			return "", best, err
		}
	}
	if err := verifyPipelineManifest(best); err != nil {
		return "", best, err
	}
	return filepath.Join(filepath.Dir(best.ManifestPath), rel), best, nil
}
