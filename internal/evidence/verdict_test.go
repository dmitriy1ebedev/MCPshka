package evidence

import "testing"

// MCP-088: a browser/gameplay smoke and the delivered artifact are gates of
// their own; without them a green build is not DONE.
func TestVerdictNeedsBrowserAndDelivery(t *testing.T) {
	for _, g := range []string{"browser", "delivery"} {
		found := false
		for _, k := range Gates {
			found = found || k == g
		}
		if !found {
			t.Fatalf("gate %s not known", g)
		}
	}
	st := func(pairs ...string) []GateState {
		var out []GateState
		for i := 0; i < len(pairs); i += 2 {
			out = append(out, GateState{Gate: pairs[i], State: pairs[i+1]})
		}
		return out
	}
	cases := map[string][]GateState{
		"DONE":         st("build", "PASS", "browser", "PASS", "delivery", "PASS"),
		"BLOCKED":      st("build", "PASS", "browser", "MISSING", "delivery", "PASS"),
		"NOT_RELEASED": st("build", "PASS", "browser", "PASS", "delivery", "MISSING"),
		"FAILED":       st("build", "PASS", "browser", "FAIL", "delivery", "MISSING"),
	}
	for want, s := range cases {
		if got := Verdict(s); got != want {
			t.Errorf("%v: %s want %s", s, got, want)
		}
	}
	if Verdict(st("build", "PASS", "browser", "STALE")) != "BLOCKED" {
		t.Fatal("stale evidence accepted")
	}
}
