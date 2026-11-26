package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/distributedci"
	"mcpshka/internal/integrationqueue"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mcpshka/internal/shipci"
	"mcpshka/internal/unitbus"
)

// integration_ship drives one commit through the whole integration conveyor:
// enqueue → sealed test → launch → fence → (explicit integration_approve) →
// worker → recover → publish. Each call performs the next safe step through the same wrapped
// tools as a direct call (tool_invoke), so authorization, Face ID, retry keys,
// receipts and admission run exactly as before. Retry keys are derived from
// the commit and step, so a lost reply or a restart is just another call.

type IntegrationShipInput struct {
	Path           string `json:"path" jsonschema:"Canonical repository root, e.g. /root/repos/mcpshka."`
	Branch         string `json:"branch" jsonschema:"Branch that holds the candidate commit."`
	Commit         string `json:"commit" jsonschema:"Full 40-hex candidate commit."`
	TargetBranch   string `json:"target_branch,omitempty" jsonschema:"Default main."`
	AdminSessionID string `json:"admin_session_id" jsonschema:"Active Face ID administrative session."`
	ApprovalID     string `json:"approval_id,omitempty" jsonschema:"ID from integration_approve; ship stops at READY_FOR_APPROVAL until it is given."`
	Evidence       string `json:"evidence,omitempty" jsonschema:"distributed (package shards across independent CI sites), module (default), gitverse, ci (GitHub) or auto: GitVerse, with GitHub as fallback when the GitVerse site itself fails; the first passing site is sealed. Passed to integration_test_prepare."`
	Attempt        int    `json:"attempt,omitempty" jsonschema:"Test run 1-9 (default 1); raise it to retest the same commit after a failed run."`
	Scope          string `json:"scope,omitempty" jsonschema:"ROADMAP scope of the integration item (default P7)."`
	ID             string `json:"id,omitempty" jsonschema:"Integration item ID; default ship-<commit[:12]>."`
}

type IntegrationShipResult struct {
	Stage      string `json:"stage"`
	Done       bool   `json:"done"`
	Commit     string `json:"commit"`
	ItemID     string `json:"item_id"`
	PermitID   string `json:"permit_id,omitempty"`
	LaunchID   string `json:"launch_id,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	Detail     string `json:"detail,omitempty"`
	NextAction string `json:"next_action"`
}

var shipCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Env isolates ship's host observations for tests.
type Env struct {
	Architecture func(ctx context.Context, repo, branch, commit string) error
	Head         func(ctx context.Context, repo, branch string) (string, error)
	Roadmap      func(repo string) (string, error)
	Active       func(ctx context.Context, unit string) bool
	UnitState    func(ctx context.Context, unit string) (string, error)
	Now          func() time.Time
}

func DefaultEnv() Env {
	return Env{
		Head: func(ctx context.Context, repo, branch string) (string, error) {
			out, err := exec.CommandContext(ctx, "/usr/bin/git", "-C", repo, "rev-parse", "--verify", "refs/heads/"+branch).Output()
			return strings.TrimSpace(string(out)), err
		},
		Roadmap: func(repo string) (string, error) {
			raw, err := os.ReadFile(filepath.Join(repo, "ROADMAP.md"))
			if err != nil {
				return "", err
			}
			sum := sha256.Sum256(raw)
			return hex.EncodeToString(sum[:]), nil
		},
		Active: func(ctx context.Context, unit string) bool {
			return exec.CommandContext(ctx, "/usr/bin/systemctl", "is-active", "--quiet", unit).Run() == nil
		},
		UnitState: func(ctx context.Context, unit string) (string, error) {
			child, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			out, err := unitbus.Output(child, "show", "--property=ActiveState", "--value", unit)
			return strings.TrimSpace(string(out)), err
		},
		Now: time.Now,
	}
}

// canary runs executor-canary <action> and decodes its JSON reply.
type Call = shipci.Call

func Canary(ctx context.Context, call Call, workdir, action, permit, launch, key string) (map[string]any, error) {
	job, err := call(ctx, "exec_start", map[string]any{"command": "mcpshka", "args": []any{"executor-canary", action, permit, launch}, "workdir": workdir, "idempotency_key": key})
	if err != nil {
		return nil, err
	}
	id, _ := job["id"].(string)
	if id == "" {
		return nil, errors.New("executor-canary: no job id")
	}
	text, err := call(ctx, "job_output", map[string]any{"job_id": id})
	if err != nil {
		return nil, err
	}
	body, _ := text["text"].(string)
	out := map[string]any{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &out); err != nil {
		return nil, fmt.Errorf("executor-canary %s: %s", action, strings.TrimSpace(body))
	}
	return out, nil
}

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

func Step(ctx context.Context, env Env, in IntegrationShipInput, call Call) (IntegrationShipResult, error) {
	if !shipCommit.MatchString(in.Commit) || in.Path == "" || in.Branch == "" || in.AdminSessionID == "" {
		return IntegrationShipResult{}, errors.New("integration_ship needs path, branch, full commit and admin_session_id")
	}
	if in.TargetBranch == "" {
		in.TargetBranch = "main"
	}
	if in.Evidence == "" {
		in.Evidence = "module"
	}
	if in.Attempt == 0 {
		in.Attempt = 1
	}
	if in.Scope == "" {
		in.Scope = "P7"
	}
	if in.ID == "" {
		in.ID = integrationqueue.DefaultShipID(in.Commit)
	}
	out := IntegrationShipResult{Commit: in.Commit, ItemID: in.ID}
	// MCP-269: the provider CI and sealed root evidence are currently pinned
	// to the MCPshka repository. Never query its CI for another project's
	// commit or report a cross-project CI_PUBLISHED as if it were valid.
	// The offline module evidence route remains available for supported Go
	// projects, subject to its own root-gated approval and tests.
	trustedVentaCI := in.Path == "/root/repos/venta-aw730-homeassistant" && in.Evidence == "ci"
	if in.Path != "/root/repos/mcpshka" && !trustedVentaCI && in.Evidence != "module" {
		out.Stage = "CI_PROVENANCE_UNSUPPORTED"
		out.Detail = "root-verified provider CI evidence is not configured for this project (MCP-269)"
		out.NextAction = "add and independently test a trusted per-project provider CI profile before protected integration; do not bypass Face ID or protected main"
		return out, nil
	}
	// The item ID is part of the key: a new item for the same commit (after a
	// cancel) replayed the old item's worker launch (BUG-20261104-37).
	key := func(step string) string {
		return "ship:" + in.ID + ":" + in.Commit + ":" + strconv.Itoa(in.Attempt) + ":" + step
	}
	again := func(stage, detail string) (IntegrationShipResult, error) {
		out.Stage, out.Detail, out.NextAction = stage, detail, "call integration_ship again with the same arguments in ~60 s"
		return out, nil
	}

	// 1. Admission: an integration item pinned to the live target head.
	status, err := call(ctx, "integration_status", map[string]any{"path": in.Path, "id": in.ID})
	if err != nil {
		return out, err
	}
	item, _ := status["item"].(map[string]any)
	switch {
	case status["found"] != true:
		head, err := env.Head(ctx, in.Path, in.TargetBranch)
		if err != nil || !shipCommit.MatchString(head) {
			return out, errors.New("target branch head unreadable: HOLD")
		}
		// The ROADMAP pin is part of the key: a refused enqueue (ROADMAP over
		// the size limit) burnt the key and the fixed retry conflicted
		// (BUG-20261023-72).
		sum, err := env.Roadmap(in.Path)
		if err != nil {
			return out, fmt.Errorf("ROADMAP.md unreadable at %s: HOLD; it lives only in the canonical repository root, so pass that root as path and the worktree's branch as branch", in.Path)
		}
		if _, err := call(ctx, "integration_enqueue", map[string]any{"path": in.Path, "id": in.ID, "branch": in.Branch, "commit": in.Commit, "target_branch": in.TargetBranch, "target_head": head, "roadmap_sha256": sum, "scope": in.Scope, "idempotency_key": key("enqueue:" + sum[:min(len(sum), 16)])}); err != nil {
			return out, err
		}
	case Str(item, "request", "commit") != in.Commit:
		return out, errors.New("integration item belongs to another commit: use a new id")
	case Str(item, "state") == "published":
		out.Stage, out.Done, out.NextAction = "PUBLISHED", true, "integration complete; deploy separately"
		return out, nil
	case Str(item, "state") == "cancelled":
		return out, errors.New("integration item is cancelled: use a new id")
	}
	// An already claimed item must be allowed to reconcile its Git effect.
	state := integrationqueue.State(Str(item, "state"))
	if in.Path == "/root/repos/mcpshka" && env.Architecture != nil && state != integrationqueue.Integrating && state != integrationqueue.Integrated {
		if err := env.Architecture(ctx, in.Path, in.TargetBranch, in.Commit); err != nil {
			out.Stage, out.Detail = "ARCHLINT_FAILED", err.Error()
			out.NextAction = "fix or cancel the affected integration intents, then submit the verified tree"
			return out, nil
		}
	}

	// 2. Sealed test evidence for the exact commit. Polling needs a fresh key.
	bucket := strconv.FormatInt(env.Now().Unix()/20, 10)
	// MCPshka drives CI itself (owner 03.11.2026: «не только ИИ-агенты, но и
	// сама MCPshka»): with GitVerse evidence it publishes the snapshot, fans
	// it out to MosHub and GitHub, and waits for the run before sealing.
	// MCPshka's forge_ci publisher is for the MCPshka repository. For
	// the explicitly trusted Venta project, the ROOT preparation worker
	// independently fetches authenticated GitHub PR checks, verifies exact
	// local/remote source-tree equality and seals that result. Do not call
	// the MCPshka CI publisher for a foreign source SHA.
	if !trustedVentaCI {
		gate := shipci.Gate{Call: call, Commit: in.Commit, Key: key, Bucket: bucket}
		var stage, detail, next string
		switch in.Evidence {
		case "distributed":
			stage, detail, next = gate.Distributed(ctx)
		case "ci":
			stage, detail, next = gate.GitHub(ctx)
		case "gitverse":
			stage, detail, next, _ = gate.GitVerse(ctx)
		case "auto":
			in.Evidence, stage, detail, next = gate.Auto(ctx)
		}
		if stage != "" {
			out.Stage, out.Detail, out.NextAction = stage, detail, next
			return out, nil
		}
	}
	prep, err := call(ctx, "integration_test_prepare", map[string]any{"admin_session_id": in.AdminSessionID, "path": in.Path, "commit": in.Commit, "evidence": in.Evidence, "attempt": in.Attempt, "idempotency_key": key("prepare:" + bucket)})
	if err != nil {
		return out, err
	}
	out.PermitID, out.LaunchID = Str(prep, "permit_id"), Str(prep, "launch_id")
	if Str(prep, "state") == "FAILED" {
		out.Stage, out.Detail = "TEST_FAILED", "sealed test preparation failed: "+Str(prep, "reason")
		out.NextAction = Str(prep, "next_action")
		return out, nil
	}
	if Str(prep, "state") != "SEALED" {
		return again("TEST_PREPARING", "sealed test is being prepared ("+Str(prep, "state")+")")
	}
	st, err := Canary(ctx, call, in.Path, "status", out.PermitID, out.LaunchID, key("status:"+bucket))
	if err != nil {
		return out, err
	}
	launches, _ := st["launches"].([]any)
	result, _ := st["result"].(map[string]any)
	switch {
	case Str(st, "state") == "ACTIVE" && len(launches) == 0:
		if _, err := Canary(ctx, call, in.Path, "launch", out.PermitID, out.LaunchID, key("launch")); err != nil {
			return out, err
		}
		return again("TEST_LAUNCHED", "sealed test started")
	case Str(st, "state") == "ACTIVE" && result == nil:
		if env.UnitState == nil || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(out.LaunchID) {
			return again("TEST_UNKNOWN", "evidence worker unit cannot be observed")
		}
		state, err := env.UnitState(ctx, "mcpshka-executor-"+out.LaunchID+".service")
		if err != nil {
			return again("TEST_UNKNOWN", "evidence worker unit observation unavailable")
		}
		if state == "inactive" || state == "failed" {
			// Close the race with a result committed just before the unit exited.
			fresh, err := Canary(ctx, call, in.Path, "status", out.PermitID, out.LaunchID, key("lost-status:"+bucket))
			if err != nil {
				return out, err
			}
			if fresh["result"] != nil {
				return again("TEST_FINISHING", "result recorded while observing the stopped evidence worker")
			}
			out.Stage, out.Detail = "TEST_LOST", "sealed test unit is "+state+" without a result; reconcile the old permit"
			out.NextAction = "repeat integration_ship with attempt " + strconv.Itoa(in.Attempt+1) + " after reconciling the lost test permit"
			if in.Attempt >= 9 {
				out.NextAction = "test attempt limit reached; reconcile the lost test permit and prepare a new commit"
			}
			return out, nil
		}
		if state != "active" && state != "activating" && state != "deactivating" && state != "reloading" {
			return again("TEST_UNKNOWN", "evidence worker unit state is unknown")
		}
		return again("TEST_RUNNING", "sealed test is running")
	case Str(st, "state") == "ACTIVE":
		if _, err := Canary(ctx, call, in.Path, "canary-fence", out.PermitID, out.LaunchID, key("fence")); err != nil {
			out.Stage, out.Detail = "TEST_FENCE_UNVERIFIED", "result recorded, but the original worker stop or cgroup could not be verified: "+err.Error()
			out.NextAction = "reconcile the original test permit with executor-canary canary-recover; repeat integration_ship with the same attempt after the fence is verified"
			return out, nil
		}
		return again("TEST_FENCING", "result recorded; fence verified, including workers stopped by systemd")
	case Str(st, "state") != "FENCED":
		return again("TEST_"+Str(st, "state"), "waiting for the executor")
	case Str(st, "result", "state") != "SUCCEEDED":
		out.Stage, out.NextAction = "TEST_FAILED", "fix the commit, or repeat with attempt "+strconv.Itoa(in.Attempt+1)+" for a flaky run"
		out.Detail = "sealed test " + Str(st, "result", "state")
		if in.Evidence == "module" && strings.Contains("\n"+Str(st, "result", "output")+"\n", "\n"+distributedci.ArchlintFailed+"\n") {
			out.Detail = "archlint failed on the sealed candidate; the remaining tests were skipped"
			out.NextAction = "inspect the sealed test output and fix the architecture violations before resubmitting"
		}
		return out, nil
	}

	if Str(item, "state") == string(integrationqueue.Integrated) {
		if head, err := env.Head(ctx, in.Path, in.TargetBranch); err != nil || head != in.Commit {
			return out, errors.New("published integration no longer matches target HEAD: HOLD")
		}
		out.Stage, out.Done, out.NextAction = "PUBLISHED", true, "integration complete; prepare the release separately"
		return out, nil
	}

	// 3. Review stays an explicit act: ship never approves. The owner (or the
	// agent acting in the owner's Face ID window) calls integration_approve
	// after reading the target..candidate diff and passes its id back here.
	if in.ApprovalID == "" {
		out.Stage = "READY_FOR_APPROVAL"
		out.Detail = "sealed test SUCCEEDED; review the diff " + in.TargetBranch + ".." + in.Commit
		out.NextAction = "call integration_approve(path, id=" + in.ID + ", store_id, permit_id=" + out.PermitID + "), then integration_ship again with approval_id"
		return out, nil
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(in.ApprovalID) {
		return out, errors.New("approval_id must be the 64-hex id returned by integration_approve")
	}
	out.ApprovalID = in.ApprovalID
	ids := map[string]any{"permit_id": out.PermitID, "approval_id": out.ApprovalID, "workdir": in.Path}
	with := func(k, v string) map[string]any {
		m := map[string]any{k: v}
		for a, b := range ids {
			m[a] = b
		}
		if k == "action" {
			m["idempotency_key"] = key("worker")
		}
		return m
	}

	// 4. Integration worker, then root reconciliation once its unit stopped.
	launch, err := call(ctx, "integration_worker_launch", with("action", "launch"))
	if err != nil {
		return out, err
	}
	if id := Str(launch, "runtime", "launch", "launch_id"); id != "" && env.Active(ctx, "mcpshka-executor-"+id+".service") {
		// A worker that recorded its effect stays alive until it is fenced;
		// waiting for the unit to stop held ship for its whole 13 min runtime
		// (BUG-20261024-95).
		// The launch reply is an idempotent replay of the first call and never
		// carries a later result; read the live worker state (BUG-20261024-95).
		live, err := call(ctx, "integration_worker_status", with("action", "status"))
		if err != nil {
			return out, err
		}
		if Str(live, "runtime", "worker", "result", "state") == "EXITED_UNFENCED" {
			fence := with("action", "fence")
			fence["idempotency_key"] = key("worker-fence")
			if _, err := call(ctx, "integration_worker_fence", fence); err != nil {
				return out, err
			}
			return again("WORKER_FENCING", "worker effect recorded; fence requested")
		}
		return again("WORKER_RUNNING", "integration worker is applying the fast-forward")
	}
	claim, err := call(ctx, "integration_recovery_status", ids)
	if err != nil {
		return out, err
	}
	if Str(claim, "state") != "RELEASED" {
		claim, err = call(ctx, "integration_recover", with("idempotency_key", key("recover:"+bucket)))
		if err != nil {
			// A worker that recorded its effect but died unfenced (timeout
			// under load) holds recovery until it is recovered itself
			// (BUG-20261023-79); then reconcile once more.
			rec := with("action", "recover")
			rec["idempotency_key"] = key("worker-recover:" + bucket)
			if _, werr := call(ctx, "integration_worker_recover", rec); werr != nil {
				// Surface the worker's own reason instead of a bare HOLD.
				if live, e := call(ctx, "integration_worker_status", with("action", "status")); e == nil {
					if why := Str(live, "runtime", "worker", "result", "output"); strings.Contains(why, "held or failed") {
						return out, errors.New(why + "; release the claim with integration_worker_abort, cancel this item and ship a new id")
					}
				}
				return out, err
			}
			if claim, err = call(ctx, "integration_recover", with("idempotency_key", key("recover2:"+bucket))); err != nil {
				return out, err
			}
		}
		if Str(claim, "state") != "RELEASED" {
			return again("WORKER_RECONCILING", "claim "+Str(claim, "state"))
		}
	}

	// 5. Fast-forward the working checkout; a repeat after success is a no-op.
	pub, err := call(ctx, "integration_publish_main", with("idempotency_key", key("publish")))
	if err != nil {
		return out, err
	}
	out.Stage, out.Done, out.Detail = "PUBLISHED", true, Str(pub, "state")
	out.NextAction = "integration complete; prepare the release separately"
	// GitVerse main stayed behind until repointed by hand; the mirror job only
	// fast-forwards and its failure never undoes the integration.
	if in.TargetBranch == "main" {
		for _, site := range []string{"gitverse", "github", "sourcecraft"} {
			k := key("mirror")
			if site != "gitverse" {
				k = key("mirror:" + site)
			}
			if job, err := call(ctx, "forge_ci", map[string]any{"provider": site, "mode": "mirror", "commit": in.Commit, "idempotency_key": k}); err == nil {
				out.Detail += "; " + site + " main mirror job " + Str(job, "id")
			} else {
				out.Detail += "; " + site + " main mirror not started: " + err.Error()
			}
		}
	}
	return out, nil
}
