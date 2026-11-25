package rootbroker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApprovedPublicReceiptFitsBrokerEnvelope(t *testing.T) {
	r := ApprovedResult{ID: strings.Repeat("a", 32), State: "DONE", Output: strings.Repeat("\x01", 8192),
		Plan: ApprovedPlan{Executable: "/usr/bin/true", Args: []string{strings.Repeat("x", 15000)}}}
	raw, err := approvedPublicJSON(r)
	if err != nil || !json.Valid(raw) || len(raw) > maxOutputBytes {
		t.Fatal("receipt overflows broker envelope", len(raw), err)
	}
	var got struct {
		Output    string
		Truncated bool `json:"output_truncated"`
	}
	if json.Unmarshal(raw, &got) != nil || !got.Truncated || len(got.Output) > 2048 {
		t.Fatal("wire truncation missing", got)
	}
	if len(r.Output) != 8192 {
		t.Fatal("wire view modified durable receipt")
	}
}
