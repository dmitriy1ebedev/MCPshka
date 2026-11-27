package projectinsights

import (
	"errors"
	"strings"
)

type BranchCheck struct {
	State              string `json:"state"`
	Expected           string `json:"expected,omitempty"`
	Observed           string `json:"observed,omitempty"`
	ClaimRevisionState string `json:"claim_revision_state"`
}

// CheckBranch compares explicit expectations, never infers ownership from a name.
func CheckBranch(actual, expected, claimRevision, revision string) (BranchCheck, error) {
	out := BranchCheck{State: "UNVERIFIED", Expected: expected, Observed: actual, ClaimRevisionState: "UNKNOWN"}
	if len(expected) > 256 || strings.ContainsAny(expected, "\x00\n\r") {
		return out, errors.New("Недопустимая ожидаемая ветка")
	}
	if expected != "" {
		out.State = "UNKNOWN"
		if actual != "" {
			out.State = "MATCH"
			if expected != actual {
				out.State = "MISMATCH"
			}
		}
	}
	if claimRevision != "" && revision != "" {
		out.ClaimRevisionState = "MATCH"
		if claimRevision != revision {
			out.ClaimRevisionState = "MISMATCH"
		}
	}
	return out, nil
}
