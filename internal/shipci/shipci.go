// Package shipci is the CI gate of integration_ship: it drives the CI sites
// through the forge_ci tool and says when the exact commit passed. With
// evidence=auto (0.9.8, owner 09.11: «запасной CI») GitVerse and GitHub run
// side by side and the first site that passes is sealed; a code failure on
// any site stops, a failure of the GitVerse site itself reruns it while
// GitHub keeps going.
package shipci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Call invokes one MCPshka tool with arguments and returns its result.
type Call func(ctx context.Context, tool string, args map[string]any) (map[string]any, error)

// Gate checks one commit. Key derives retry keys; Bucket changes every poll.
type Gate struct {
	Call   Call
	Commit string
	Key    func(string) string
	Bucket string
}

const again = "call integration_ship again with the same arguments in ~60 s"

// leaseHeld turns a forge_ci refusal by a held project lease into its own
// stage: polling cannot clear it and it is not a failure of the CI site.
// It was reported as CI_OBSERVING and sealed as UNKNOWN (MCP-166 appendix).
func leaseHeld(err error) (stage, detail, next string, ok bool) {
	if err == nil || !strings.Contains(err.Error(), "OWNER_BUSY") || !strings.Contains(err.Error(), "lease") {
		return "", "", "", false
	}
	return "LEASE_HELD", "the project lease is held, so forge_ci was refused: " + err.Error(),
		"release your project lease (project_lease_release) or wait for its holder, then call integration_ship again", true
}

// Str reads a nested string field.
func Str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

// Observe runs one forge_ci job and decodes its JSON reply.
func (g Gate) Observe(ctx context.Context, provider, mode, key string) (map[string]any, error) {
	job, err := g.Call(ctx, "forge_ci", map[string]any{"provider": provider, "mode": mode, "commit": g.Commit, "idempotency_key": key})
	if err != nil {
		return nil, err
	}
	id := Str(job, "id")
	if id == "" {
		return nil, errors.New("forge_ci: no job id")
	}
	if _, err := g.Call(ctx, "job_wait", map[string]any{"job_id": id, "timeout_seconds": 40}); err != nil {
		return nil, err
	}
	text, err := g.Call(ctx, "job_output", map[string]any{"job_id": id})
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(Str(text, "text"))
	if body == "" {
		body = strings.TrimSpace(Str(text, "stdout"))
	}
	if i := strings.LastIndex(body, "\n{"); i >= 0 {
		body = body[i+1:]
	}
	obs := map[string]any{}
	if json.Unmarshal([]byte(body), &obs) != nil {
		return nil, fmt.Errorf("forge_ci %s %s: no result yet", provider, mode)
	}
	return obs, nil
}

// GitHub makes GitHub a full CI site: MCPshka pushes the snapshot, opens the
// pull request its workflows need and waits for every required workflow
// before root seals the check runs. An empty stage means CI passed.
func (g Gate) GitHub(ctx context.Context) (stage, detail, next string) {
	st, err := g.Observe(ctx, "github", "status", g.Key("gh-status:"+g.Bucket))
	if stage, detail, next, ok := leaseHeld(err); ok {
		return stage, detail, next
	}
	if err != nil {
		return "CI_OBSERVING", "GitHub CI status unavailable: " + err.Error(), again
	}
	switch {
	case Str(st, "state") == "observed_pass":
		return "", "", ""
	case Str(st, "state") == "observed_fail" && Str(st, "diagnosis", "code") == "CI_ACCOUNT_BLOCKED":
		// S5.DIAGCI: GitHub refused the jobs before any runner; no commit
		// fixes that, so say so instead of «fix the failure».
		return "CI_ACCOUNT_BLOCKED", "GitHub rejected every job before a runner: Actions minutes/billing of the account", "restore GitHub Actions minutes, or ship with evidence=module (sealed test on H96)"
	case Str(st, "state") == "observed_fail":
		return "CI_FAILED", "GitHub checks failed for this commit", "fix the failure, commit, then integration_ship with the new commit"
	case Str(st, "code") == "no_run_for_commit":
		for _, mode := range []string{"publish", "pr"} {
			if _, err := g.Observe(ctx, "github", mode, g.Key("gh-"+mode)); err != nil {
				return "CI_PUBLISHING", "GitHub " + mode + " failed: " + err.Error(), again
			}
		}
		return "CI_PUBLISHED", "MCPshka published the snapshot to GitHub and opened its pull request into main", again
	default:
		return "CI_RUNNING", "GitHub CI is running for this commit", again
	}
}

// GitVerse gates sealing on GitVerse CI for the exact commit. An empty stage
// means CI passed; envFail reports a failure of the site, not of the code.
func (g Gate) GitVerse(ctx context.Context) (stage, detail, next string, envFail bool) {
	st, err := g.Observe(ctx, "gitverse", "status", g.Key("ci-status:"+g.Bucket))
	if stage, detail, next, ok := leaseHeld(err); ok {
		return stage, detail, next, false
	}
	if err != nil {
		return "CI_OBSERVING", "GitVerse CI status unavailable: " + err.Error(), again, true
	}
	runs, _ := st["runs"].([]any)
	failure, _ := st["failure"].(map[string]any)
	switch {
	case Str(st, "state") == "observed_pass":
		return "", "", "", false
	case failure != nil || Str(st, "state") == "observed_fail":
		d := "GitVerse CI failed for this commit"
		if jobs, ok := failure["jobs"].([]any); ok {
			for _, j := range jobs {
				jm, _ := j.(map[string]any)
				if ex, ok := jm["excerpt"].([]any); ok && len(ex) > 0 {
					d += ": " + fmt.Sprint(ex[0])
					break
				}
			}
		}
		env := failure != nil && Str(failure, "failure_class") != "TEST_FAILURE"
		return "CI_FAILED", d, "fix the failure, commit, then integration_ship with the new commit (do not rerun the same commit)", env
	case cancelledStreak(runs, st["recent_outcomes"]) >= stuckAfter:
		// ROADMAP S5.DIAGCI: GitVerse cancelled 34 runs in a row (17–23.11)
		// while every poll republished or reran. Three in a row is the site,
		// not the code: stop touching it, file one card, let GitHub decide.
		card := "card not filed"
		if out, err := g.Call(ctx, "issue_create", map[string]any{"title": "GitVerse CI cancels every run of " + short(g.Commit), "priority": "P2", "category": "CI", "force": true, "source": "integration_ship", "body": "GitVerse cancelled the last " + fmt.Sprint(stuckAfter) + " runs of commit " + g.Commit + " in a row; MCPshka stopped republishing and rerunning it. Check the site (runner, quota, private repos) before the next publish.", "idempotency_key": g.Key("ci-stuck-card")}); err == nil {
			card = "card " + Str(out, "issue", "id")
		}
		return "CI_STUCK", "GitVerse cancelled the last " + fmt.Sprint(stuckAfter) + " runs in a row: no more republish/rerun (" + card + ")", "the site is broken, not the code: GitHub evidence, or integration_ship evidence=module after local tests", false
	case Str(st, "state") == "HOLD" && Str(st, "diagnosis", "code") == "CI_PLATFORM_CANCELLED":
		// MCP-271: provider cancellation is not a running job or a code
		// failure. Preserve the root CI gate; Auto may check independent
		// GitHub evidence and dispatch one idempotent provider rerun.
		return "CI_FAILED", "GitVerse CI provider cancelled execution; no source test verdict", again, true
	case len(runs) == 0:
		if _, err := g.Call(ctx, "forge_ci", map[string]any{"provider": "gitverse", "mode": "publish", "commit": g.Commit, "idempotency_key": g.Key("ci-publish:gitverse")}); err != nil {
			return "CI_PUBLISHING", "GitVerse publish failed: " + err.Error(), again, true
		}
		fan := []string{"gitverse"}
		for _, site := range []string{"moshub", "github"} {
			if _, err := g.Call(ctx, "forge_ci", map[string]any{"provider": site, "mode": "publish", "commit": g.Commit, "idempotency_key": g.Key("ci-publish:" + site)}); err == nil {
				fan = append(fan, site)
			}
		}
		return "CI_PUBLISHED", "MCPshka published the snapshot for CI: " + strings.Join(fan, ", "), again, false
	case noRunner(runs):
		// No runner took any job (GitVerse quota or outage, 16–18.11: hours in
		// "waiting"): a site failure, so auto starts GitHub beside it.
		return "CI_NO_RUNNER", "GitVerse CI is waiting for a runner", again, true
	default:
		return "CI_RUNNING", "GitVerse CI is running for this commit", again, false
	}
}

// stuckAfter identical provider cancellations stop the gate's retries.
const stuckAfter = 3

// cancelledStreak counts the newest runs (first in the list) cancelled in a row.
// cancelledStreak counts the newest cancelled runs. ci_status lists only the
// newest run in runs; its recent_outcomes (newest three) carry the streak,
// without them the stop could never see more than one cancellation.
func cancelledStreak(runs []any, recent any) int {
	n := 0
	if list, ok := recent.([]any); ok {
		for _, s := range list {
			if s != "cancelled" && s != "canceled" {
				break
			}
			n++
		}
		return n
	}
	for _, r := range runs {
		if m, _ := r.(map[string]any); Str(m, "status") != "cancelled" {
			break
		}
		n++
	}
	return n
}

func short(sha string) string { return sha[:min(len(sha), 12)] }

// noRunner: every run of the commit still waits for a runner.
func noRunner(runs []any) bool {
	for _, r := range runs {
		if m, _ := r.(map[string]any); Str(m, "status") != "waiting" {
			return false
		}
	}
	return len(runs) > 0
}

// Auto returns the evidence kind to seal ("gitverse" or "ci") once a site
// passed, or the stage to report while none has.
func (g Gate) Auto(ctx context.Context) (evidence, stage, detail, next string) {
	gvStage, gvDetail, gvNext, envFail := g.GitVerse(ctx)
	if gvStage == "" {
		return "gitverse", "", "", ""
	}
	if gvStage == "CI_FAILED" && !envFail {
		return "", gvStage, gvDetail, gvNext
	}
	if gvStage == "CI_PUBLISHED" || gvStage == "CI_RUNNING" || gvStage == "LEASE_HELD" {
		// GitHub runs in parallel only when GitVerse is unhealthy: its
		// minutes are scarce, and a healthy GitVerse is enough.
		return "", gvStage, gvDetail, gvNext
	}
	if gvStage == "CI_FAILED" {
		// The site failed, not the code: one rerun of the same snapshot.
		if _, err := g.Call(ctx, "forge_ci", map[string]any{"provider": "gitverse", "mode": "rerun", "commit": g.Commit, "idempotency_key": g.Key("gv-rerun")}); err == nil {
			gvDetail += "; rerun dispatched once"
		}
	}
	ghStage, ghDetail, ghNext := g.GitHub(ctx)
	if ghStage == "" {
		return "ci", "", "", ""
	}
	if ghStage == "CI_FAILED" {
		return "", ghStage, ghDetail, ghNext
	}
	return "", "CI_FALLBACK", "GitVerse: " + gvDetail + " — fallback GitHub: " + ghDetail + "; the first site that passes is sealed", again
}
