package forge

import (
	"context"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/forgeci"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/tooldef"
)

type ForgeCIInput struct {
	Provider       string `json:"provider" jsonschema:"distributed (four package shards: GitHub, GitVerse, MosHub, SourceCraft across healthy sites), gitverse, moshub, sourcecraft, gitflic, github or gigaide (runner only) — interchangeable CI sites; github pushes as the mcpshka service user over HTTPS with its own token."`
	Mode           string `json:"mode" jsonschema:"runner (wake a temporary pull runner on github/gitverse/moshub or open the configured GigaIDE workspace; no commit needed), plan (local identity only), publish (push the immutable snapshot mcpshka-snapshots/<commit>, which starts that site's CI) or status (read that site's CI for the snapshot); gitverse rerun dispatches snapshot-contracts on the immutable snapshot branch, moshub rerun retries a failed snapshot pipeline; gitverse/sourcecraft/github mirror fast-forwards that site's main to this commit, which must be local main (never forced); github pr opens the pull request into main that GitHub's gating workflows need."`
	Commit         string `json:"commit,omitempty" jsonschema:"Not required for mode=runner. Full 40-hex SHA of any commit in the canonical MCPshka repository, including worktree branches."`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Required retry key; reusing it returns the original job."`
	LeaseToken     string `json:"lease_token,omitempty" jsonschema:"Optional current interactive lease token: the holder may run CI jobs in its own leased project."`
	Path           string `json:"path,omitempty" jsonschema:"DIAG/Lane: provider=distributed mode=plan|publish|status|wait, four sites, remote build artifacts with SHA-256, no H96 fallback. Other H96 project (/root/repos/<name>): provider=gitverse mode=publish mirrors that commit to its PRIVATE GitVerse repo (created on first use, never public; a mirror: its main is set exactly to this commit, H96 is the source of truth; with .mcpshka/publish.json {\"mode\":\"code_only\",\"keep\":[…]} the site gets instead one parentless snapshot with code and test inputs only — no README, docs, changelogs or history; status and rerun find it by the same source commit) and starts its .gitverse/workflows CI; mode=status reads that CI (state NO_RUN/RUNNING/PASS/FAIL). Full checks run there instead of loading H96."`
	Workdir        string `json:"workdir,omitempty" jsonschema:"Optional clean immediate sibling worktree sharing the canonical Git store, with its current holder token and HEAD pinned to commit; only plan/publish/status. Never for protected main mirror/PR or deployment."`
}

func JobInput(in ForgeCIInput) ([]string, jobs.StartOptions, error) {
	return forgeci.JobInput(in.Provider, in.Mode, in.Commit, in.IdempotencyKey, in.LeaseToken, in.Path)
}

// forgeCIWorkdir permits only a pinned, independently leased sibling of the
// canonical repository for immutable CI observations/snapshot publication.
// It does NOT make mirror, PR, protected integration, or deploy independent.
func Workdir(in ForgeCIInput, root string) (string, error) {
	return forgeci.Workdir(in.Workdir, root, in.Mode, in.LeaseToken, in.Commit)
}

func Register(add func(tooldef.Def), m interface {
	StartWithOptions(string, []string, string, jobs.StartOptions) (jobs.Snapshot, error)
}, store *idempotency.Store, owner func(context.Context) string, digest func(string) string) {
	closed := false
	add(tooldef.New(&mcp.Tool{
		Name:        "forge_ci",
		Description: "provider=distributed: plan computes deterministic package shards; publish probes and dispatches missing shards with UTC-day rotation and infrastructure failover; status returns shard/provider/state/seconds table. UNTRUSTED; root independently re-fetches for evidence=distributed. mode=runner wakes a temporary GitHub/GitVerse/MosHub pull runner or opens GigaIDE (MCPSHKA_GIGAIDE_WORKSPACE_URL); commit is optional, no snapshot is published. Test any MCPshka commit on another CI site without GitHub minutes: publish its immutable snapshot to GitVerse, MosHub, SourceCraft or GitFlic (their CI starts on the snapshot branch), retry the same snapshot with gitverse mode=rerun (workflow_dispatch), keep the site's main in step with mode=mirror (gitverse, sourcecraft, github), then read that CI with mode=status. The sites are interchangeable: when one is down, use another. One bounded job; wait with job_wait, result is the JSON line of job_output. An independent immediate sibling Git worktree may provide its own lease_token and workdir for immutable plan/publish/status only; root writer remains required for mirror/PR/integration/deploy. Any other H96 project (DIAG, Lane…): pass path=/root/repos/<project> with provider=gitverse mode=publish, then mode=wait and job_wait until it ends (a whole CI run in one job; status = one look; infra=true: the CI site failed, not the code → mode=rerun, same commit) — its private GitVerse repo runs the full checks so H96 is not loaded. A passing GitVerse run is sealed for integration with integration_test_prepare evidence=gitverse. UNTRUSTED observation; never authorizes deployment.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closed},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in ForgeCIInput) (*mcp.CallToolResult, jobs.Snapshot, error) {
		args, opts, err := JobInput(in)
		if err != nil {
			return nil, jobs.Snapshot{}, err
		}
		workdir, err := Workdir(in, forgeci.Root)
		if len(args) == 5 { // another project: its own directory
			workdir, err = filepath.Clean(in.Path), nil
		}
		if err != nil {
			return nil, jobs.Snapshot{}, err
		}
		key := owner(ctx) + "\x00" + in.IdempotencyKey
		payload := in
		payload.LeaseToken = digest(in.LeaseToken)
		out, err := idempotency.Run(store, key, "forge_ci", payload, func() (jobs.Snapshot, error) {
			return m.StartWithOptions(forgeci.Command, args, workdir, opts)
		})
		return nil, out, err
	}))
}
