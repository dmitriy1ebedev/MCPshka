package failure

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFailureWireRejectsUnboundedOrContradictoryServerFacts(t *testing.T) {
	for _, detail := range []*Error{
		{Class: FailedVerified, Code: strings.Repeat("x", 97), NoEffect: true},
		{Class: Class(strings.Repeat("x", 1000)), Code: "FIXTURE", NoEffect: true},
		{Class: FencedUnknown, Code: "FIXTURE", NoEffect: true},
		{Class: FailedVerified, Code: "FIXTURE", NoEffect: true, Hold: true},
	} {
		if proof := ForWire(detail); proof != nil {
			t.Fatal("invalid wire proof emitted", len(proof.Code), len(proof.Class))
		}
	}
	if ForWire(errors.New("no_effect=true")) != nil {
		t.Fatal("text acquired typed authority")
	}
	detail := Rejected("FIXTURE", "fixture", "private error body", nil)
	detail.NextSafeAction = Action{Tool: "private", Arguments: map[string]any{"token": "private-credential"}}
	raw, err := json.Marshal(ForWire(detail))
	if err != nil || len(raw) > 256 || strings.Contains(string(raw), "private") {
		t.Fatal("wire budget/privacy", len(raw), err)
	}
	if got := DecodeWire(raw, "original message"); !IsNoEffect(got) {
		t.Fatal("round trip lost no-effect", got)
	}
}
