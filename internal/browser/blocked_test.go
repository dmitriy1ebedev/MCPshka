package browser

import "testing"

// MCP-090: a browser that cannot start is BLOCKED with a cause, never an
// application failure, and a partial smoke is never reported as e2e.
func TestChromiumUnavailableIsBlockedNotFailed(t *testing.T) {
	cases := map[string]string{
		"fork/exec chromium: resource temporarily unavailable":   "RESOURCE_EXHAUSTED",
		"RuntimeError: can't start new thread":                   "RESOURCE_EXHAUSTED",
		"exec: \"chromium\": executable file not found in $PATH": "CHROMIUM_MISSING",
		"devtools endpoint: context deadline exceeded":           "LAUNCH_TIMEOUT",
		"chromium exited with status 21":                         "LAUNCH_FAILED",
	}
	for msg, want := range cases {
		r := blocked(Result{State: "failed", FailureStage: "launch_chromium", Error: msg})
		if r.State != "blocked" || r.BlockedReason != want || r.Coverage != "none" || r.Alternative == "" {
			t.Fatalf("%q: %+v", msg, r)
		}
	}
}
