package gitprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mcpshka/internal/distributedci"
	"mcpshka/internal/opencore"
)

var publicCI = map[Site]struct {
	target opencore.Target
	route  string
}{
	GitHub:      {opencore.GitHubTarget("dmitriy1ebedev/MCPshka"), "/repos/dmitriy1ebedev/MCPshka"},
	GitVerse:    {opencore.Target{Site: "gitverse", URL: "git@gitverse.ru:dmitriy_1ebedev/MCPshka.git", Auth: "ssh", Key: "/var/lib/mcpshka/gitverse/id_ed25519", KnownHosts: "/var/lib/mcpshka/gitverse/known_hosts"}, "/repos/dmitriy_1ebedev/MCPshka"},
	MosHub:      {opencore.Target{Site: "moshub", URL: "https://hub.mos.ru/lda6994/mcpshka.git", Auth: "basic:oauth2", TokenFile: "/var/lib/mcpshka/moshub/api_token"}, "/projects/lda6994%2Fmcpshka"},
	SourceCraft: {opencore.Target{Site: "sourcecraft", URL: "https://git.sourcecraft.dev/lda6994/mcpshka.git", Auth: "bearer", TokenFile: "/var/lib/mcpshka/sourcecraft/token"}, "/repos/lda6994/mcpshka"},
}

// PublicCIProviders shares the staged, audited export across shard dispatches.
func PublicCIProviders(repo string, plan distributedci.Plan, snapshot *opencore.CISnapshot) []distributedci.Provider {
	out := DistributedProviders(repo, plan)
	for _, provider := range out {
		provider.(*CIProvider).snapshot = snapshot
	}
	return out
}

func (p *CIProvider) route() string {
	if p.public {
		return publicCI[p.Client.site].route
	}
	return p.Client.ciRoute()
}

func (p *CIProvider) startPublic(ctx context.Context, plan distributedci.Plan, n int) error {
	target, ok := publicCI[p.Client.site]
	if !ok || !p.public || plan.Digest == "" || n < 0 || n >= len(plan.Shards) {
		return distributedci.ErrEvidence
	}
	if err := publishPublicShard(ctx, p.Repo, p.journalDir(plan.Commit), plan, n, target.target, p.snapshot); err != nil {
		return err
	}
	if p.Client.site != SourceCraft {
		return nil
	}
	// SourceCraft reads push triggers on main. Dispatch this pinned branch
	// with the workflow from the audited snapshot instead.
	payload := map[string]any{
		"head":            map[string]string{"branch": distributedci.ShardRef(plan, n)},
		"config_revision": map[string]string{"commit": plan.Snapshot},
		"workflows":       []map[string]string{{"name": "distributed"}},
	}
	if err := p.Client.ciJSON(ctx, "POST", p.route()+"/cicd/runs", payload, nil); err != nil {
		return distributedci.ErrDispatchUnknown
	}
	return nil
}

func publishPublicShard(ctx context.Context, repo, journal string, plan distributedci.Plan, n int, target opencore.Target, s *opencore.CISnapshot) error {
	if s == nil || s.Manifest.Source != plan.Commit || s.Tree != plan.Tree || s.Manifest.Digest != plan.Digest {
		return distributedci.ErrEvidence
	}
	found := false
	for _, configured := range s.Manifest.Targets {
		if configured == target {
			found = true
			break
		}
	}
	if !found {
		return distributedci.ErrEvidence
	}
	body, err := distributedci.PublicSnapshotBody(ctx, repo, plan.Commit, plan.Tree, plan.Digest)
	if err != nil {
		return distributedci.ErrEvidence
	}
	ref := "refs/heads/" + distributedci.ShardRef(plan, n)
	if err = os.MkdirAll(journal, 0700); err != nil {
		return err
	}
	path := filepath.Join(journal, target.Site+"-"+strconv.Itoa(n))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return distributedci.ErrDispatchUnknown
	}
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "\n" + plan.Snapshot + "\n")
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	if err = s.PushCI(ctx, target, ref, plan.Snapshot, body); err != nil {
		if errors.Is(err, opencore.ErrCIPushUnverified) {
			return distributedci.ErrDispatchUnknown
		}
		_ = os.Remove(path)
		if errors.Is(err, opencore.ErrBlocked) {
			return distributedci.ErrEvidence
		}
		return err
	}
	return nil
}
