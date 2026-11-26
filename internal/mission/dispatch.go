package mission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"mcpshka/internal/evidence"
	"mcpshka/internal/healthlayer"
)

type DispatchOutcome string

const DispatchIntent DispatchOutcome = "INTENT"
const DispatchAccepted DispatchOutcome = "ACCEPTED"

// INTENT is a durable proposal, not an execution ACK or effect proof.
// Recovery probes use Tool/Key; transport loss never creates another key.
type DispatchRecord struct {
	Step             string             `json:"step"`
	Scope            string             `json:"scope"`
	Key              string             `json:"dispatch_key"`
	Fingerprint      string             `json:"step_fingerprint"`
	ContractRevision string             `json:"contract_revision"`
	Inputs           evidence.Inputs    `json:"inputs"`
	Action           healthlayer.Action `json:"action"`
	Outcome          DispatchOutcome    `json:"outcome"`
}

func dispatchFingerprint(a healthlayer.Action) string {
	raw, _ := json.Marshal(a)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func (s *Snapshot) sealDispatch(step string, proposed healthlayer.Action) healthlayer.Action {
	for _, d := range s.Dispatches {
		if d.Step == step {
			return d.Action
		}
	}
	var args struct {
		Key string `json:"idempotency_key"`
	}
	json.Unmarshal(proposed.Args, &args)
	s.Dispatches = append(s.Dispatches, DispatchRecord{Step: step, Scope: s.Path, Key: args.Key, Fingerprint: dispatchFingerprint(proposed), ContractRevision: s.CapabilityRevision, Inputs: s.Inputs, Action: proposed, Outcome: DispatchIntent})
	return proposed
}
func (s *Snapshot) validateDispatches() error {
	if len(s.Dispatches) > len(s.Steps) {
		return errors.New("mission dispatch budget exceeded: HOLD")
	}
	seen := map[string]bool{}
	for _, d := range s.Dispatches {
		var args struct {
			Key string `json:"idempotency_key"`
		}
		if json.Unmarshal(d.Action.Args, &args) != nil || d.Key == "" || args.Key != d.Key || d.Scope != s.Path || d.Fingerprint != dispatchFingerprint(d.Action) || seen[d.Step] || d.Outcome != DispatchIntent && d.Outcome != DispatchAccepted {
			return errors.New("mission dispatch identity changed: HOLD")
		}
		seen[d.Step] = true
	}
	return nil
}
