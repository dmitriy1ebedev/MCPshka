package rootbroker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApprovedWireBinaryOutputCannotExpandBudget(t *testing.T) {
	original := ApprovedResult{Output: strings.Repeat(string([]byte{255}), 2048)}
	raw, err := approvedPublicJSON(original)
	if err != nil {
		t.Fatal(err)
	}
	var got ApprovedResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Output) > 2048 || !got.OutputTruncated {
		t.Fatalf("binary replacement expanded output to %d bytes; truncated=%v", len(got.Output), got.OutputTruncated)
	}
	if len(original.Output) != 2048 {
		t.Fatal("public projection changed durable record")
	}
}
