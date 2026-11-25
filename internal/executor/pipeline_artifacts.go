package executor

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"mcpshka/internal/pipelineartifact"
)

// Recipe package artifacts: the file mechanics live in internal/pipelineartifact;
// the executor binds them to fenced, successful permits.

type PipelineArtifact = pipelineartifact.Artifact

func pipelineErr(e error) error {
	if errors.Is(e, pipelineartifact.ErrRejected) {
		return ErrHold
	}
	return e
}

func pipelineDir(permit string) string {
	return filepath.Join(AuthorityRoot, "pipeline-artifacts", permit)
}

// The worker seals only declared regular files, before disposable scratch cleanup.
func sealPipelineArtifacts(paths []string, scratch, dest string, owner uint32) error {
	if filepath.Dir(dest) == filepath.Join(AuthorityRoot, "pipeline-artifacts") {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := PrunePipelineArtifacts(ctx)
		cancel()
		if err != nil {
			return err
		}
	}
	return pipelineErr(pipelineartifact.Seal(paths, scratch, dest, owner))
}

func PipelineReceipt(ctx context.Context, permit string) ([]PipelineArtifact, error) {
	m, err := successfulLocalPermit(ctx, permit)
	if err != nil {
		return nil, err
	}
	var spec RunSpec
	for _, s := range m.Runs {
		spec = s
	}
	if m.Purpose != "project-recipe-test-v1" || spec.Stage != "package" || len(spec.Artifacts) == 0 {
		return nil, ErrHold
	}
	raw, err := localRead(filepath.Join(pipelineDir(permit), "receipt.json"), 16384, 0)
	if err != nil {
		return nil, err
	}
	a, err := pipelineartifact.CheckReceipt(raw, spec.Artifacts)
	return a, pipelineErr(err)
}

func PipelineRead(ctx context.Context, permit string, index int, offset int64) ([]byte, bool, error) {
	artifacts, err := PipelineReceipt(ctx, permit)
	if err != nil {
		return nil, false, err
	}
	if index < 0 || index >= len(artifacts) {
		return nil, false, ErrHold
	}
	raw, eof, err := pipelineartifact.ReadAt(filepath.Join(pipelineDir(permit), strconv.Itoa(index)), artifacts[index].Bytes, offset)
	return raw, eof, pipelineErr(err)
}

// PrunePipelineArtifacts removes only sealed, fenced receipts older than seven days.
func PrunePipelineArtifacts(ctx context.Context) error {
	return pipelineartifact.Prune(ctx, filepath.Join(AuthorityRoot, "pipeline-artifacts"), func(ctx context.Context, permit string) bool {
		c, m, e := OpenPermit(permit)
		if e != nil {
			return false
		}
		state, e := c.Inspect(ctx, m.Pin)
		return e == nil && state.State == "FENCED"
	})
}
