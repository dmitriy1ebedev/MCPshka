package failure

import (
	"encoding/json"
	"errors"
)

const WireSchema = "mcpshka.failure/v1"
const MetadataKey = "mcpshka/failure"

// Wire carries diagnostic facts without input, credentials or action arguments.
type Wire struct {
	Schema   string `json:"schema"`
	Class    Class  `json:"class"`
	Code     string `json:"reason_code"`
	Hold     bool   `json:"hold,omitempty"`
	NoEffect bool   `json:"no_effect,omitempty"`
}

func ForWire(err error) *Wire {
	detail := Details(err)
	if detail == nil {
		return nil
	}
	w := &Wire{Schema: WireSchema, Class: detail.Class, Code: detail.Code, Hold: detail.Hold, NoEffect: detail.NoEffect}
	if !w.valid() {
		return nil
	}
	return w
}

// DecodeWire preserves only versioned server facts; prose never proves NoEffect.
func DecodeWire(raw json.RawMessage, message string) error {
	var w Wire
	if json.Unmarshal(raw, &w) != nil || !w.valid() {
		return errors.New(message)
	}
	return &Error{Class: w.Class, Disposition: Disposition(w.Class), Code: w.Code, Site: "mcp.tool", Message: message, Hold: w.Hold, NoEffect: w.NoEffect}
}

func (w Wire) valid() bool {
	if w.Schema != WireSchema || w.Code == "" || len(w.Code) > 96 {
		return false
	}
	switch w.Class {
	case QuotaExhausted, Disconnected, OwnerBusy, AuthRequired, PolicyDenied, ApprovalRequired, FencedUnknown, Transient, FailedVerified, EvidenceMissing, Deadline:
	default:
		return false
	}
	return !w.NoEffect || !w.Hold && w.Class != FencedUnknown && w.Class != EvidenceMissing
}
