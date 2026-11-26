package codexcoord

import "testing"

func TestDiagnosticEnvelopeCompatibility(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{`{"state":"CLAIMED"}`, 9},
		{`{"diagnostic":{"stage":"ready","error_class":""}}`, 10},
		{`{"diagnostic":{"stage":"ready","stage":"create","error_class":""}}`, -1},
		{`{"diagnostic":{"stage":"ready","error_class":"","extra":"secret"}}`, -1},
		{`{"diagnostic":"secret"}`, -1},
	} {
		if got := SessionEventFields([]byte(tc.raw)); got != tc.want {
			t.Fatal(tc.raw, got)
		}
	}
	if ValidDiagnostic(&Diagnostic{Stage: "raw-secret", ErrorClass: "raw-secret"}) {
		t.Fatal("unbounded diagnostic")
	}
}
