package rootbroker

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestApprovedWireTailHasBoundedValidUTF8(t *testing.T) {
	r := ApprovedResult{Output: strings.Repeat("界", 3000)}
	raw, err := approvedPublicJSON(r)
	var out ApprovedResult
	if err != nil || json.Unmarshal(raw, &out) != nil {
		t.Fatal("invalid receipt", err)
	}
	if !utf8.ValidString(out.Output) || len(out.Output) > 2048 {
		t.Fatal("wire tail exceeds byte budget after Unicode replacement", len(out.Output))
	}
	if len(r.Output) != 9000 {
		t.Fatal("durable receipt changed")
	}
}
