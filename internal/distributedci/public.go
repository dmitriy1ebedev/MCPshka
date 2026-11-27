package distributedci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"mcpshka/internal/opencore"
)

var digestRE = regexp.MustCompile("^[a-f0-9]{64}$")

// LoadPublicPlan plans and pins the audited MCPshka export for this commit.
func LoadPublicPlan(ctx context.Context, repo, commit string) (Plan, error) {
	s, err := opencore.StageCI(ctx, repo, commit, opencore.HostRules(ctx))
	if err != nil {
		return Plan{}, err
	}
	defer s.Close()
	return PlanPublicSnapshot(ctx, repo, commit, s)
}

// PlanPublicSnapshot inventories an export already staged for publication.
func PlanPublicSnapshot(ctx context.Context, repo, commit string, s *opencore.CISnapshot) (Plan, error) {
	if s == nil || s.Manifest.Source != commit {
		return Plan{}, ErrEvidence
	}
	for _, path := range []string{".github/workflows/distributed.yml", ".gitverse/workflows/distributed.yml", ".gitlab-ci.yml", ".sourcecraft/ci.yaml"} {
		if _, err := os.Stat(filepath.Join(s.Dir, path)); err != nil {
			return Plan{}, fmt.Errorf("HOLD: public shard workflow unavailable: %s", path)
		}
	}
	if err := s.AllowInventory(); err != nil {
		return Plan{}, err
	}
	packages, counts, weights, err := inventory(ctx, s.Dir)
	if err != nil {
		return Plan{}, err
	}
	dir, err := materialize(ctx, repo, commit)
	if err != nil {
		return Plan{}, err
	}
	defer os.RemoveAll(dir)
	originals, _, _, err := inventory(ctx, dir)
	if err != nil {
		return Plan{}, err
	}
	missing := 0
	for _, pkg := range originals {
		if !slices.Contains(packages, pkg) {
			missing++
		}
	}
	if missing != 0 || !slices.Equal(packages, originals) {
		return Plan{}, fmt.Errorf("HOLD: export package inventory differs (%d packages need private CI)", missing)
	}
	body, err := PublicSnapshotBody(ctx, repo, commit, s.Tree, s.Manifest.Digest)
	if err != nil {
		return Plan{}, err
	}
	p, err := NewPlan(commit, s.Tree, snapshotSHA(body), packages, weights, counts, len(Sites))
	p.Digest = s.Manifest.Digest
	return p, err
}

func PublicSnapshotBody(ctx context.Context, repo, commit, tree, digest string) (string, error) {
	if !shaRE.MatchString(commit) || !digestRE.MatchString(digest) {
		return "", errors.New("invalid public snapshot identity")
	}
	raw, err := git(ctx, repo, "cat-file", "commit", commit)
	if err != nil {
		return "", err
	}
	return SnapshotBody(commit, tree, raw, digest)
}

func checkoutDigest(ctx context.Context, dir string) (string, error) {
	msg, err := git(ctx, dir, "show", "-s", "--format=%B")
	if err != nil {
		return "", err
	}
	digest := ""
	for _, line := range strings.Split(msg, "\n") {
		if value, ok := strings.CutPrefix(line, "Export-Digest: "); ok {
			if digest != "" || !digestRE.MatchString(value) {
				return "", ErrEvidence
			}
			digest = value
		}
	}
	return digest, nil
}

func CanonicalTree(ctx context.Context, repo, commit string) (string, error) {
	if !shaRE.MatchString(commit) {
		return "", ErrEvidence
	}
	return git(ctx, repo, "rev-parse", commit+"^{tree}")
}
