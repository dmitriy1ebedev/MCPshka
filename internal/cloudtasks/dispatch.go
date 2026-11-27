package cloudtasks

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
)

const dispatchPrefix = "cloudtasks/dispatch/"

var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)
var dispatchPR = regexp.MustCompile(`^https://github.com/dmitriy1ebedev/mcpshka(?:-ci)?/pull/[1-9][0-9]*$`)

// DispatchRequest is sent only to the owner-bound administrative broker.
// The journal stores its digest, never instructions or account credentials.
type DispatchRequest struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Provider   string `json:"provider"`
	Commit     string `json:"commit"`
	Prompt     string `json:"prompt"`
	Acceptance string `json:"acceptance"`
}

func DispatchID(owner, retryKey string) string {
	return "dispatch-" + hashIdentity([]string{owner, retryKey})[:32]
}

func (p DispatchRequest) Validate() error {
	if !validID.MatchString(p.ID) || p.Owner == "" || len(p.Owner) > 256 ||
		(p.Provider != "codex_cloud" && p.Provider != "claude_cloud") || !commitID.MatchString(p.Commit) ||
		len(strings.TrimSpace(p.Prompt)) == 0 || len(p.Prompt) > 2000 ||
		len(strings.TrimSpace(p.Acceptance)) == 0 || len(p.Acceptance) > 800 ||
		strings.ContainsRune(p.Prompt+p.Acceptance, 0) {
		return errors.New("cloud dispatch requires provider, full commit, prompt (1..2000 bytes) and acceptance (1..800 bytes)")
	}
	raw, _ := json.Marshal(p)
	if len(raw) > 4096 {
		return errors.New("encoded cloud request exceeds broker argument budget")
	}
	return nil
}

type DispatchReceipt struct {
	ID               string `json:"id"`
	Owner            string `json:"owner"`
	Provider         string `json:"provider"`
	Commit           string `json:"commit"`
	RequestSHA256    string `json:"request_sha256"`
	State            string `json:"state"`
	Reason           string `json:"reason,omitempty"`
	SnapshotCommit   string `json:"snapshot_commit,omitempty"`
	SourceTree       string `json:"source_tree,omitempty"`
	ProviderTaskID   string `json:"provider_task_id,omitempty"`
	ProviderState    string `json:"provider_state,omitempty"`
	ResultBranch     string `json:"result_branch,omitempty"`
	ResultCommit     string `json:"result_commit,omitempty"`
	PullRequest      string `json:"pull_request,omitempty"`
	Trust            string `json:"trust"`
	DeployAuthorized bool   `json:"deploy_authorized"`
}

// Claim commits UNKNOWN before the first external mutation. A lost reply or
// crash never causes an automatic second submission, even after a restart.
func (j *Journal) Claim(p DispatchRequest) (DispatchReceipt, bool, error) {
	if err := p.Validate(); err != nil {
		return DispatchReceipt{}, false, err
	}
	r := DispatchReceipt{ID: p.ID, Owner: p.Owner, Provider: p.Provider, Commit: p.Commit,
		RequestSHA256: hashIdentity(p), State: "unknown", Reason: "dispatch_outcome_not_reconciled", Trust: "UNTRUSTED"}
	raw, _ := json.Marshal(r)
	won, err := j.db.PutIfAbsentBounded(dispatchPrefix+p.ID, raw, dispatchPrefix, maxRecords)
	if err != nil {
		return DispatchReceipt{}, false, err
	}
	if won {
		return r, true, nil
	}
	prior, err := j.Dispatch(p.ID)
	if err != nil {
		return DispatchReceipt{}, false, err
	}
	if prior.RequestSHA256 != r.RequestSHA256 || prior.Owner != p.Owner {
		return DispatchReceipt{}, false, errors.New("cloud dispatch retry payload conflicts: HOLD")
	}
	return prior, false, nil
}

func (j *Journal) Dispatch(id string) (DispatchReceipt, error) {
	if !validID.MatchString(id) {
		return DispatchReceipt{}, errors.New("invalid dispatch ID")
	}
	raw, err := j.db.Get(dispatchPrefix + id)
	if err != nil {
		return DispatchReceipt{}, err
	}
	var r DispatchReceipt
	if json.Unmarshal(raw, &r) != nil || r.ID != id || validateDispatch(r) != nil {
		return r, errors.New("invalid durable dispatch: HOLD")
	}
	return r, nil
}

func validateDispatch(r DispatchReceipt) error {
	if !validID.MatchString(r.ID) || !validDigest.MatchString(r.RequestSHA256) ||
		r.Trust != "UNTRUSTED" || r.DeployAuthorized || !commitID.MatchString(r.Commit) ||
		(r.Provider != "codex_cloud" && r.Provider != "claude_cloud") || r.Owner == "" {
		return errors.New("invalid durable dispatch: HOLD")
	}
	switch r.State {
	case "unknown", "blocked", "submitted", "running", "awaiting_pr", "failed", "result":
	default:
		return errors.New("invalid dispatch state: HOLD")
	}
	if len(r.Reason) > 200 || len(r.ProviderState) > 50 {
		return errors.New("dispatch metadata budget: HOLD")
	}
	if r.State != "blocked" && r.State != "unknown" &&
		(!commitID.MatchString(r.SnapshotCommit) || !commitID.MatchString(r.SourceTree) || !validID.MatchString(r.ProviderTaskID)) {
		return errors.New("provider task and source pins required: HOLD")
	}
	if r.State == "result" && (r.ResultBranch != "cloud-agents/"+r.ID || !commitID.MatchString(r.ResultCommit) || !dispatchPR.MatchString(r.PullRequest)) {
		return errors.New("same-repository result branch/PR required: HOLD")
	}
	if r.State != "result" && (r.ResultBranch != "" || r.ResultCommit != "" || r.PullRequest != "") {
		return errors.New("non-result contains result identity: HOLD")
	}
	return nil
}

// Finish uses CAS so concurrent polls cannot overwrite a different receipt.
func (j *Journal) Finish(old, next DispatchReceipt) (DispatchReceipt, error) {
	if err := validateDispatch(next); err != nil {
		return old, err
	}
	if old.ID != next.ID || old.Owner != next.Owner || old.Provider != next.Provider ||
		old.Commit != next.Commit || old.RequestSHA256 != next.RequestSHA256 ||
		next.Trust != "UNTRUSTED" || next.DeployAuthorized ||
		(old.ProviderTaskID != "" && old.ProviderTaskID != next.ProviderTaskID) ||
		(old.SnapshotCommit != "" && old.SnapshotCommit != next.SnapshotCommit) ||
		(old.SourceTree != "" && old.SourceTree != next.SourceTree) {
		return old, errors.New("cloud dispatch identity changed: HOLD")
	}
	if old.State == "result" || old.State == "failed" || old.State == "blocked" {
		return old, errors.New("terminal dispatch: HOLD")
	}
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(next)
	won, err := j.db.Replace(dispatchPrefix+old.ID, a, b)
	if err != nil {
		return old, err
	}
	if !won {
		return old, errors.New("cloud dispatch CAS conflict: inspect cloud_task_status")
	}
	return j.Dispatch(old.ID)
}

func (j *Journal) DispatchList(limit int) ([]DispatchReceipt, error) {
	if limit < 1 || limit > 20 {
		return nil, errors.New("cloud task list budget 1..20")
	}
	ids, err := j.db.List(dispatchPrefix)
	if err != nil {
		return nil, err
	}
	if len(ids) > maxRecords {
		return nil, errors.New("dispatch capacity: HOLD")
	}
	out := make([]DispatchReceipt, 0, limit)
	for _, id := range ids {
		r, e := j.Dispatch(strings.TrimPrefix(id, dispatchPrefix))
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
