package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/jobs"
	"regexp"
	"strings"
)

var executorCanaryID = regexp.MustCompile("^[0-9a-f]{32}$")

// executorCanaryCompat operates only on a root-prepared synthetic runtime
// fixture. It cannot prepare a permit, transfer a project lease or select code.
func executorCanaryCompat(ctx context.Context, m *jobs.Manager, in ExecStartInput) (jobs.Snapshot, error) {
	action, _, e := executorCanaryRequest(in)
	if e != nil {
		return jobs.Snapshot{}, e
	}
	if _, e := m.Policy().CheckWorkdir(in.Workdir); e != nil {
		return jobs.Snapshot{}, e
	}
	brokerAction := action
	switch action {
	case "canary-abort":
		// Explicit abort keeps the old fence semantics: stop now, no result.
		brokerAction = "canary-fence"
	case "canary-fence":
		// Fencing an active launch before its result is recorded discards the
		// evidence (BUG-20261021-34); wait instead, or abort deliberately.
		raw, e := brokerclient.Run(ctx, "executor", "status", in.Args[2], in.Args[3])
		if e != nil {
			return jobs.Snapshot{}, e
		}
		var s struct {
			State    string          `json:"state"`
			Result   json.RawMessage `json:"result"`
			Launches []struct {
				State string `json:"state"`
			} `json:"launches"`
		}
		if json.Unmarshal(raw, &s) != nil {
			return jobs.Snapshot{}, errors.New("executor status unreadable: HOLD")
		}
		if s.State == "ACTIVE" && len(s.Launches) > 0 && (len(s.Result) == 0 || string(s.Result) == "null") {
			return jobs.Snapshot{}, errors.New("launch has no recorded result yet: poll executor_runtime_status (or action status) until result appears, then canary-fence; use canary-abort to stop it deliberately")
		}
	}
	out, e := brokerclient.Run(ctx, "executor", brokerAction, in.Args[2], in.Args[3])
	if e != nil {
		return jobs.Snapshot{}, e
	}
	return m.StoreReceipt("mcpshka", []string{"executor-canary", action, in.Args[2], in.Args[3]}, in.Workdir, string(out))
}

func executorCanaryRequest(in ExecStartInput) (string, ExecutorRuntimeInput, error) {
	if len(in.Args) != 4 || len(in.Env) != 0 || in.ApprovalToken != "" || in.PlanHash != "" || strings.TrimSpace(in.IdempotencyKey) == "" {
		return "", ExecutorRuntimeInput{}, errors.New("executor-canary requires action, permit, launch, retry key and no overrides")
	}
	action := in.Args[1]
	if action != "launch" && action != "status" && action != "canary-fence" && action != "canary-abort" && action != "canary-recover" {
		return "", ExecutorRuntimeInput{}, errors.New("unsupported synthetic executor action")
	}
	if !executorCanaryID.MatchString(in.Args[2]) || !executorCanaryID.MatchString(in.Args[3]) {
		return "", ExecutorRuntimeInput{}, errors.New("invalid synthetic executor identities")
	}
	return action, ExecutorRuntimeInput{PermitID: in.Args[2], LaunchID: in.Args[3], Workdir: in.Workdir}, nil
}
