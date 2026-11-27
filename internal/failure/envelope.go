package failure

import (
	"encoding/json"
	"errors"
)

// Envelope contains the server's evidence about a failed tool call. Missing
// handles and receipts stay null until the server can attest to them.
type Envelope struct {
	ErrorCode         string   `json:"error_code"`
	FailureLayer      string   `json:"failure_layer"`
	TaskID            *string  `json:"task_id"`
	JobID             *string  `json:"job_id"`
	Receipt           any      `json:"receipt"`
	ActualEffect      string   `json:"actual_effect"`
	RetryAllowed      bool     `json:"retry_allowed"`
	RetryAfterSeconds int      `json:"retry_after_seconds,omitempty"`
	Blocker           string   `json:"blocker"`
	NextSafeAction    Action   `json:"next_safe_action"`
	RemainingWork     []string `json:"remaining_work"`
}

const GoCacheMaintenance = "GO_CACHE_MAINTENANCE"

var layers = map[string]string{
	"INVALID_ARGUMENT":       "client",
	"CLIENT_SCHEMA_STALE":    "client",
	"CAPABILITY_MISSING":     "model_choice",
	GoCacheMaintenance:       "host",
	"TRANSPORT_DISCONNECTED": "transport",
	"CONTEXT_CANCELED":       "transport",
	"CONTEXT_DEADLINE":       "transport",
	string(Disconnected):     "transport",
	string(QuotaExhausted):   "external",
}

type beforeDispatch struct{ cause error }

func (e beforeDispatch) Error() string { return e.cause.Error() }
func (e beforeDispatch) Unwrap() error { return e.cause }

// BeforeDispatch preserves the caller's existing error text and attests that
// the tool handler was never reached.
func BeforeDispatch(err error) error { return beforeDispatch{cause: err} }

func ToolEnvelope(err error, blocker string, noEffect bool) Envelope {
	out := Envelope{ErrorCode: "TOOL_ERROR", Blocker: blocker, ActualEffect: "unknown", NextSafeAction: DefaultAction(FencedUnknown)}
	if err != nil {
		var rejected beforeDispatch
		noEffect = noEffect || errors.As(err, &rejected)
		var classified *Classified
		if errors.As(err, &classified) {
			out.ErrorCode = string(classified.Class)
			_ = json.Unmarshal([]byte(classified.Next), &out.NextSafeAction)
		}
		if detail := Details(err); detail != nil {
			out.ErrorCode = detail.Code
			out.NextSafeAction = detail.NextSafeAction
			out.Receipt = detail.Receipt
			if detail.TaskID != "" {
				out.TaskID = &detail.TaskID
			}
			if detail.JobID != "" {
				out.JobID = &detail.JobID
			}
			if detail.EffectApplied && detail.Receipt != nil && !detail.Hold {
				out.ActualEffect = "applied"
			}
			out.RetryAllowed = detail.RetryAfterSeconds > 0 && detail.NoEffect
			out.RetryAfterSeconds = detail.RetryAfterSeconds
			noEffect = noEffect || detail.NoEffect
		}
	}
	if out.ErrorCode == "" {
		out.ErrorCode = "TOOL_ERROR"
	}
	if out.NextSafeAction.Tool == "" {
		out.NextSafeAction = DefaultAction(FencedUnknown)
	}
	if noEffect {
		out.ActualEffect = "none"
		out.RemainingWork = []string{"Resolve the blocker and complete the requested operation."}
	} else if out.ActualEffect == "applied" {
		out.RemainingWork = []string{"Resolve the blocker without repeating the recorded effect."}
	} else {
		out.RemainingWork = []string{"Reconcile the original outcome before retrying or reporting completion."}
	}
	out.FailureLayer = layers[out.ErrorCode]
	if out.FailureLayer == "" {
		out.FailureLayer = "mcpshka"
	}
	return out
}
