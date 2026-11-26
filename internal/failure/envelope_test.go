package failure

import (
	"errors"
	"testing"
)

func TestToolEnvelopeUsesEvidenceForActualEffect(t *testing.T) {
	policy := Classify(BeforeDispatch(errors.New("lacks scope")), nil)
	hold := NewHold(FencedUnknown, "UNVERIFIED_RUN", "fixture", "cannot see original effect", nil)
	hold.Receipt = map[string]string{"state": "pending"}
	applied := &Error{Code: "POSTCHECK_FAILED", EffectApplied: true, JobID: "job-fixture", Receipt: map[string]string{"state": "done"}}
	for _, tc := range []struct {
		err                 error
		code, layer, effect string
	}{
		{policy, "POLICY_DENIED", "mcpshka", "none"},
		{hold, "UNVERIFIED_RUN", "mcpshka", "unknown"},
		{applied, "POSTCHECK_FAILED", "mcpshka", "applied"},
		{errors.New("no effect and safe to retry"), "TOOL_ERROR", "mcpshka", "unknown"},
		{Rejected("INVALID_ARGUMENT", "fixture", "invalid payload", nil), "INVALID_ARGUMENT", "client", "none"},
	} {
		out := ToolEnvelope(tc.err, tc.err.Error(), false)
		if out.ErrorCode != tc.code || out.FailureLayer != tc.layer || out.ActualEffect != tc.effect || out.RetryAllowed ||
			out.NextSafeAction.Tool == "" || out.Blocker == "" || len(out.RemainingWork) == 0 {
			t.Errorf("%+v", out)
		}
	}
}
